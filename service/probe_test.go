package main

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestProbeTCPConnectsAndExcludesResolutionTime(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			_ = connection.Close()
		}
		close(accepted)
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	target := Target{ID: "local", Host: "delayed.example", Port: port, TimeoutMS: 500}
	resolver := func(context.Context, string, time.Duration) ([]string, error) {
		time.Sleep(80 * time.Millisecond)
		return []string{"127.0.0.1"}, nil
	}
	sample := probeTCPWithResolver(context.Background(), target, resolver)
	if sample.Status != StatusSuccess || sample.Latency == nil {
		t.Fatalf("probe did not connect: %#v", sample)
	}
	if *sample.Latency >= 60 {
		t.Fatalf("TCP latency appears to include the 80ms DNS delay: %.3fms", *sample.Latency)
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("listener did not accept connection")
	}
}

func TestProbeTCPReportsResolutionFailureAsDNS(t *testing.T) {
	target := Target{ID: "dns", Host: "missing.invalid", Port: 443, TimeoutMS: 500}
	resolver := func(context.Context, string, time.Duration) ([]string, error) {
		return nil, &net.DNSError{Err: "not found", Name: "missing.invalid", IsNotFound: true}
	}
	sample := probeTCPWithResolver(context.Background(), target, resolver)
	if sample.Status != StatusDNSError || sample.Latency != nil {
		t.Fatalf("unexpected DNS failure sample: %#v", sample)
	}
}

func TestClassifyProbeError(t *testing.T) {
	if got := classifyProbeError(&net.DNSError{Err: "timeout", IsTimeout: true}); got != StatusDNSError {
		t.Fatalf("DNS timeout must remain a DNS error, got %s", got)
	}
	if got := classifyProbeError(context.DeadlineExceeded); got != StatusTimeout {
		t.Fatalf("deadline must be timeout, got %s", got)
	}
	if got := classifyProbeError(errors.New("unexpected")); got != StatusOther {
		t.Fatalf("unexpected error must be other, got %s", got)
	}
}

func TestClassifyWrappedWinsockErrors(t *testing.T) {
	wrapped := func(code syscall.Errno) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connectex", Err: code}}
	}
	if got := classifyProbeError(wrapped(10061)); got != StatusRefused {
		t.Fatalf("WSAECONNREFUSED classified as %s", got)
	}
	if got := classifyProbeError(wrapped(10060)); got != StatusTimeout {
		t.Fatalf("WSAETIMEDOUT classified as %s", got)
	}
	for _, code := range []syscall.Errno{10049, 10050, 10051, 10065} {
		if got := classifyProbeError(wrapped(code)); got != StatusNoRoute {
			t.Fatalf("Winsock errno %d classified as %s", code, got)
		}
	}
	if got := classifyProbeError(wrapped(syscall.ECONNREFUSED)); got != StatusRefused {
		t.Fatalf("native ECONNREFUSED classified as %s", got)
	}
	if got := classifyProbeError(wrapped(syscall.ENETUNREACH)); got != StatusNoRoute {
		t.Fatalf("native ENETUNREACH classified as %s", got)
	}
}

func TestProbeTCPFallsBackToNextResolvedAddress(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			_ = connection.Close()
		}
		close(accepted)
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	target := Target{ID: "fallback", Host: "multi.example", Port: port, TimeoutMS: 1000}
	resolver := func(context.Context, string, time.Duration) ([]string, error) {
		return []string{"127.0.0.2", "127.0.0.1"}, nil
	}
	sample := probeTCPWithResolver(context.Background(), target, resolver)
	if sample.Status != StatusSuccess || sample.Latency == nil {
		t.Fatalf("probe did not fall back to the second address: %#v", sample)
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("listener did not accept fallback connection")
	}
}

func TestResolveHostDeduplicatesAllAddresses(t *testing.T) {
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("2001:db8::1")},
			{IP: net.ParseIP("192.0.2.10")},
			{IP: net.ParseIP("2001:db8::1")},
		}, nil
	}
	addresses, err := resolveHostWithLookup(context.Background(), "multi.example", time.Second, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 2 || addresses[0] != "2001:db8::1" || addresses[1] != "192.0.2.10" {
		t.Fatalf("unexpected resolved addresses: %#v", addresses)
	}
}

func TestProbeTCPReportsRealRefusal(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	target := Target{ID: "refused", Host: "127.0.0.1", Port: port, TimeoutMS: 500}
	sample := probeTCP(context.Background(), target)
	if sample.Status != StatusRefused {
		t.Fatalf("real loopback refusal classified as %#v", sample)
	}
}
