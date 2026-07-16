//go:build windows

package main

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWindowsExternalBypassProbe(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("NETWATCH_BYPASS_TEST_TARGET"))
	if endpoint == "" {
		t.Skip("set NETWATCH_BYPASS_TEST_TARGET=host:port to run the external acceptance probe")
	}
	host, portText, err := net.SplitHostPort(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ID: "external", Host: host, Port: port, TimeoutMS: 5000, BypassTUN: true}
	sample := probeTCP(context.Background(), target)
	if sample.Status != StatusSuccess || sample.Latency == nil {
		t.Fatalf("external bypass probe failed: %#v", sample)
	}
	t.Logf("external bypass TCP latency: %.3fms", *sample.Latency)
	target.BypassTUN = false
	ordinary := probeTCP(context.Background(), target)
	if ordinary.Status == StatusSuccess && ordinary.Latency != nil {
		t.Logf("ordinary system-route TCP latency: %.3fms", *ordinary.Latency)
	}
}

func TestWindowsBoundDialUsesSelectedPhysicalSource(t *testing.T) {
	interfaces, err := enumeratePlatformInterfaces()
	if err != nil {
		t.Fatal(err)
	}
	var selected physicalInterface
	var source net.IP
	for _, candidate := range interfaces {
		if address := candidate.addressForFamily(4); address != nil {
			selected = candidate
			source = address
			break
		}
	}
	if source == nil {
		t.Skip("no active physical IPv4 interface")
	}

	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = connection.Close()
		}
		close(accepted)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	plan := bypassDialPlan{interfaceInfo: selected, sourceIP: source, family: 4}
	port := listener.Addr().(*net.TCPAddr).Port
	connection, err := plan.dial(ctx, net.JoinHostPort(source.String(), strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("bound dial failed: %v", err)
	}
	local := connection.LocalAddr().(*net.TCPAddr)
	_ = connection.Close()
	if !local.IP.Equal(source) {
		t.Fatalf("dial used %s instead of %s", local.IP, source)
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("listener did not accept bound connection")
	}
}
