package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func main() {
	if err := runMain(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		log.Printf("NetWatch failed: %v", err)
		os.Exit(1)
	}
}

type serviceOptions struct {
	dataDir       string
	listenAddress string
	managed       bool
}

func runMain(args []string, stdin io.Reader, stdout io.Writer) error {
	dataDirDefault, err := defaultDataDir()
	if err != nil {
		return fmt.Errorf("determine data directory: %w", err)
	}
	options, err := parseServiceOptions(args, dataDirDefault)
	if err != nil {
		return err
	}
	return runService(options, stdin, stdout)
}

func parseServiceOptions(args []string, dataDirDefault string) (serviceOptions, error) {
	options := serviceOptions{}
	flags := flag.NewFlagSet("netwatch-service", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.dataDir, "data-dir", dataDirDefault, "directory used for targets.json")
	flags.StringVar(&options.listenAddress, "listen", listenAddress, "loopback address used by the local API")
	flags.BoolVar(&options.managed, "managed", false, "use the Tauri sidecar lifecycle protocol")
	if err := flags.Parse(args); err != nil {
		return serviceOptions{}, fmt.Errorf("parse arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return serviceOptions{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if strings.TrimSpace(options.dataDir) == "" {
		return serviceOptions{}, fmt.Errorf("data-dir must not be empty")
	}
	if err := validateListenAddress(options.listenAddress); err != nil {
		return serviceOptions{}, err
	}
	return options, nil
}

func validateListenAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return fmt.Errorf("invalid listen port %q", portText)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("listen address must use a loopback host")
	}
	return nil
}

func runService(options serviceOptions, stdin io.Reader, stdout io.Writer) error {
	previousLogOutput := log.Writer()
	logFile, err := configureLogging(options.dataDir)
	if err != nil {
		return fmt.Errorf("initialize logging: %w", err)
	}
	defer func() {
		log.SetOutput(previousLogOutput)
		_ = logFile.Close()
	}()

	store := NewConfigStore(options.dataDir)
	targets, exists, err := store.Load()
	if err != nil {
		return fmt.Errorf("load targets: %w", err)
	}
	if !exists {
		targets = defaultTargets()
		if err := store.Save(targets); err != nil {
			return fmt.Errorf("save default targets: %w", err)
		}
	} else if applyTargetDefaults(targets) {
		if err := store.Save(targets); err != nil {
			return fmt.Errorf("migrate target configuration: %w", err)
		}
	}

	listener, err := net.Listen("tcp", options.listenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", options.listenAddress, err)
	}
	defer listener.Close()

	hub := newEventHub()
	monitor, err := NewMonitor(targets, store, hub, defaultSampleCapacity)
	if err != nil {
		return fmt.Errorf("initialize monitor: %w", err)
	}
	defer monitor.Close()

	actualAddress := listener.Addr().String()
	api, err := NewAPIServerAt(monitor, hub, actualAddress)
	if err != nil {
		return fmt.Errorf("initialize HTTP server: %w", err)
	}
	server := api.HTTPServer()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	log.Printf("NetWatch listening on http://%s (data: %s)", actualAddress, options.dataDir)
	return serveHTTP(server, listener, serveOptions{
		managed: options.managed,
		stdin:   stdin,
		stdout:  stdout,
		signals: signals,
		ready:   api.readyMessage(actualAddress),
	})
}

func applyTargetDefaults(targets []Target) bool {
	changed := false
	for index := range targets {
		if targets[index].Kind == "" {
			targets[index].Kind = ProbeKindDirectTCP
			changed = true
		}
	}
	return changed
}

func defaultDataDir() (string, error) {
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		return filepath.Join(localAppData, "NetWatch"), nil
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if config == "" {
		return "", fmt.Errorf("user config directory is empty")
	}
	return filepath.Join(config, "NetWatch"), nil
}

func configureLogging(dataDir string) (*os.File, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(dataDir, "netwatch.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	// The file is first so a Windows GUI-subsystem build still retains logs
	// even when its stderr handle is absent. Probe samples are never logged.
	log.SetOutput(io.MultiWriter(file, os.Stderr))
	return file, nil
}

func defaultTargets() []Target {
	return []Target{
		{ID: "example-cloudflare", Name: "Cloudflare HTTPS", Kind: ProbeKindDirectTCP, Host: "1.1.1.1", Port: 443, IntervalMS: 5000, TimeoutMS: 2000, Enabled: true},
		{ID: "example-google-dns", Name: "Google DNS (TCP)", Kind: ProbeKindDirectTCP, Host: "8.8.8.8", Port: 53, IntervalMS: 5000, TimeoutMS: 2000, Enabled: true},
	}
}
