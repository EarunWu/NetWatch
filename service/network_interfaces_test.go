package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSelectPhysicalInterfacePrefersDefaultThenMetric(t *testing.T) {
	items := []physicalInterface{
		{id: "slow", name: "Slow", addresses: []net.IP{net.ParseIP("192.0.2.10")}, index4: 10, metric4: 5},
		{id: "default-high", name: "Default high", addresses: []net.IP{net.ParseIP("192.0.2.11")}, index4: 11, metric4: 50, default4: true},
		{id: "default-low", name: "Default low", addresses: []net.IP{net.ParseIP("192.0.2.12")}, index4: 12, metric4: 10, default4: true},
	}
	selected, source, err := selectPhysicalInterface(items, "", 4)
	if err != nil {
		t.Fatal(err)
	}
	if selected.id != "default-low" || !source.Equal(net.ParseIP("192.0.2.12")) {
		t.Fatalf("unexpected automatic selection: %#v %v", selected, source)
	}

	selected, _, err = selectPhysicalInterface(items, "slow", 4)
	if err != nil || selected.id != "slow" {
		t.Fatalf("manual selection was not honored: %#v %v", selected, err)
	}
}

func TestSelectPhysicalInterfaceManualFailuresDoNotFallBack(t *testing.T) {
	items := []physicalInterface{{
		id: "ipv4-only", name: "IPv4 only", addresses: []net.IP{net.ParseIP("192.0.2.10")}, index4: 10,
	}}
	if _, _, err := selectPhysicalInterface(items, "missing", 4); !isTUNBypassError(err) {
		t.Fatalf("missing manual interface did not return a bypass error: %v", err)
	}
	if _, _, err := selectPhysicalInterface(items, "ipv4-only", 6); !isTUNBypassError(err) {
		t.Fatalf("address-family mismatch did not return a bypass error: %v", err)
	}
}

func TestInterfaceCatalogCachesForFiveSecondsAndInvalidates(t *testing.T) {
	now := time.Unix(100, 0)
	loads := 0
	catalog := newInterfaceCatalog(func() ([]physicalInterface, error) {
		loads++
		return []physicalInterface{{id: "one", addresses: []net.IP{net.ParseIP("192.0.2.1")}}}, nil
	})
	catalog.now = func() time.Time { return now }
	if _, err := catalog.load(); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.load(); err != nil {
		t.Fatal(err)
	}
	if loads != 1 {
		t.Fatalf("cache loaded %d times within TTL", loads)
	}
	now = now.Add(networkInterfaceCacheTTL)
	if _, err := catalog.load(); err != nil {
		t.Fatal(err)
	}
	if loads != 2 {
		t.Fatalf("expired cache loaded %d times", loads)
	}
	catalog.invalidate()
	if _, err := catalog.load(); err != nil {
		t.Fatal(err)
	}
	if loads != 3 {
		t.Fatalf("invalidated cache loaded %d times", loads)
	}
}

func TestBypassProbeFailsClosedAndAddsFakeIPHint(t *testing.T) {
	original := bypassInterfaceCatalog
	bypassInterfaceCatalog = newInterfaceCatalog(func() ([]physicalInterface, error) {
		return nil, nil
	})
	defer func() { bypassInterfaceCatalog = original }()

	target := Target{ID: "direct", Host: "fake.example", Port: 443, TimeoutMS: 500, BypassTUN: true}
	resolver := func(context.Context, string, time.Duration) ([]string, error) {
		return []string{"198.18.0.10"}, nil
	}
	sample := probeTCPWithResolver(context.Background(), target, resolver)
	if sample.Status != StatusTUNBypassError || sample.Latency != nil {
		t.Fatalf("bypass failure silently fell back: %#v", sample)
	}
	if !strings.Contains(sample.Message, "FakeIP") {
		t.Fatalf("FakeIP guidance missing from error: %q", sample.Message)
	}
}

func TestInterfaceCatalogPropagatesDiscoveryErrors(t *testing.T) {
	wanted := errors.New("discovery failed")
	catalog := newInterfaceCatalog(func() ([]physicalInterface, error) { return nil, wanted })
	if _, err := catalog.load(); !errors.Is(err, wanted) {
		t.Fatalf("unexpected discovery error: %v", err)
	}
}
