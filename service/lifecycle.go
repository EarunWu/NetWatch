package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultShutdownTimeout = 5 * time.Second

type readyMessage struct {
	Type     string `json:"type"`
	Version  string `json:"version"`
	Protocol string `json:"protocol"`
	Instance string `json:"instance"`
	Address  string `json:"address"`
	URL      string `json:"url"`
}

func (s *APIServer) readyMessage(address string) readyMessage {
	return readyMessage{
		Type:     "ready",
		Version:  serviceVersion,
		Protocol: serviceProtocol,
		Instance: s.instanceID,
		Address:  address,
		URL:      "http://" + address,
	}
}

type serveOptions struct {
	managed         bool
	stdin           io.Reader
	stdout          io.Writer
	signals         <-chan os.Signal
	ready           readyMessage
	shutdownTimeout time.Duration
}

type managedInputResult struct {
	err error
}

// serveHTTP owns the HTTP listener lifecycle. In managed mode stdout is a
// machine-readable channel: exactly one ready object is emitted once the
// listener has been created, and stdin controls shutdown.
func serveHTTP(server *http.Server, listener net.Listener, options serveOptions) error {
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Serve(listener)
	}()

	var managedInput <-chan managedInputResult
	if options.managed {
		if options.stdout == nil {
			_ = server.Close()
			<-serverErrors
			return errors.New("managed stdout is unavailable")
		}
		if err := json.NewEncoder(options.stdout).Encode(options.ready); err != nil {
			_ = server.Close()
			<-serverErrors
			return fmt.Errorf("write managed ready message: %w", err)
		}
		managedInput = watchManagedInput(options.stdin)
	}

	select {
	case received := <-options.signals:
		log.Printf("received %s; shutting down", received)
	case result := <-managedInput:
		if result.err != nil {
			log.Printf("managed stdin failed; shutting down: %v", result.err)
		} else {
			log.Printf("managed shutdown requested")
		}
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("HTTP server failed: %w", err)
	}

	timeout := options.shutdownTimeout
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	shutdownErr := server.Shutdown(ctx)
	cancel()
	if shutdownErr != nil {
		_ = server.Close()
	}

	serveErr := <-serverErrors
	if shutdownErr != nil {
		return fmt.Errorf("HTTP shutdown: %w", shutdownErr)
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("HTTP server failed during shutdown: %w", serveErr)
	}
	return nil
}

func watchManagedInput(reader io.Reader) <-chan managedInputResult {
	result := make(chan managedInputResult, 1)
	go func() {
		if reader == nil {
			result <- managedInputResult{}
			return
		}
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			if strings.EqualFold(strings.TrimSpace(scanner.Text()), "shutdown") {
				result <- managedInputResult{}
				return
			}
		}
		// EOF is an intentional shutdown signal: it means the Tauri parent
		// closed its sidecar stdin, including during an unexpected parent exit.
		result <- managedInputResult{err: scanner.Err()}
	}()
	return result
}
