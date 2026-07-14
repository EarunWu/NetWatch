package main

import (
	"math"
	"testing"
)

func TestNormalizeAndValidateTarget(t *testing.T) {
	valid := Target{ID: "target_1", Name: " Example ", Host: " example.com ", Port: 443, Google204Enabled: true, IntervalMS: 1000, TimeoutMS: 500, Enabled: true}
	normalized, err := normalizeAndValidateTarget(valid)
	if err != nil {
		t.Fatalf("valid target rejected: %v", err)
	}
	if normalized.Name != "Example" || normalized.Host != "example.com" {
		t.Fatalf("target was not normalized: %#v", normalized)
	}
	if normalized.Kind != ProbeKindDirectTCP {
		t.Fatalf("legacy target did not default to direct TCP: %#v", normalized)
	}
	if normalized.Google204Enabled {
		t.Fatal("direct TCP target retained the node-only Google 204 option")
	}

	tests := []struct {
		name   string
		change func(*Target)
	}{
		{"empty name", func(target *Target) { target.Name = "" }},
		{"URL host", func(target *Target) { target.Host = "https://example.com" }},
		{"host with port", func(target *Target) { target.Host = "example.com:443" }},
		{"zero port", func(target *Target) { target.Port = 0 }},
		{"short interval", func(target *Target) { target.IntervalMS = minIntervalMS - 1 }},
		{"long timeout", func(target *Target) { target.TimeoutMS = maxTimeoutMS + 1 }},
		{"invalid id", func(target *Target) { target.ID = "invalid/id" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.change(&candidate)
			if _, err := normalizeAndValidateTarget(candidate); err == nil {
				t.Fatal("invalid target was accepted")
			}
		})
	}
}

func TestNormalizeProxyGoogleTarget(t *testing.T) {
	target := Target{
		ID: "node_1", Name: " Node ", Kind: ProbeKindProxyGoogle,
		Host: "ignored.example", Port: 80,
		ProxyHost: "127.0.0.1", ProxyPort: 10808,
		Google204Enabled: true,
		IntervalMS:       5000, TimeoutMS: 8000, Enabled: true,
	}
	normalized, err := normalizeAndValidateTarget(target)
	if err != nil {
		t.Fatalf("valid node target rejected: %v", err)
	}
	if normalized.Host != GoogleProbeHost || normalized.Port != GoogleProbePort || normalized.Name != "Node" || !normalized.Google204Enabled {
		t.Fatalf("Google endpoint was not normalized: %#v", normalized)
	}
	tlsOnly := normalized
	tlsOnly.Google204Enabled = false
	if sameProbeIdentity(normalized, tlsOnly) {
		t.Fatal("changing the terminal probe stage did not change probe identity")
	}

	invalid := target
	invalid.ProxyHost = "192.0.2.1"
	if _, err := normalizeAndValidateTarget(invalid); err == nil {
		t.Fatal("non-loopback proxy was accepted")
	}
	invalid = target
	invalid.IntervalMS = minNodeIntervalMS - 1
	if _, err := normalizeAndValidateTarget(invalid); err == nil {
		t.Fatal("overly frequent Google probe was accepted")
	}
}

func TestSampleRingAndStats(t *testing.T) {
	ring := newSampleRing(5)
	statuses := []string{StatusSuccess, StatusTimeout, StatusRefused, StatusDNSError, StatusNoRoute, StatusOther}
	for index, status := range statuses {
		sample := Sample{TargetID: "one", TS: int64(index + 1), Status: status}
		if status == StatusSuccess {
			latency := 12.5
			sample.Latency = &latency
		}
		ring.Add(sample)
	}
	values := ring.Values()
	if len(values) != 5 || values[0].Status != StatusTimeout || values[4].Status != StatusOther {
		t.Fatalf("ring did not retain the newest five values: %#v", values)
	}

	latency := 10.0
	samples := []Sample{
		{Status: StatusSuccess, TS: 1, Latency: &latency},
		{Status: StatusTimeout, TS: 2},
		{Status: StatusRefused, TS: 3},
		{Status: StatusDNSError, TS: 4},
		{Status: StatusNoRoute, TS: 5},
		{Status: StatusOther, TS: 6},
	}
	stats := calculateStats(samples)
	if stats.SuccessRate != 16.67 || stats.TimeoutRate != 16.67 || stats.RefusedRate != 16.67 {
		t.Fatalf("unexpected base rates: %#v", stats)
	}
	if stats.EstimatedLossRate == nil || math.Abs(*stats.EstimatedLossRate-83.33) > 0.001 {
		t.Fatalf("estimated loss must be every non-success/all samples, got %v", stats.EstimatedLossRate)
	}
	if stats.CurrentMS != nil || stats.ConsecutiveFailure != 5 || stats.LastStatus != StatusOther {
		t.Fatalf("unexpected latest state: %#v", stats)
	}
}

func TestEstimatedLossCountsEveryFailureStatus(t *testing.T) {
	stats := calculateStats([]Sample{{Status: StatusRefused}, {Status: StatusDNSError}, {Status: StatusNoRoute}, {Status: StatusOther}})
	if stats.EstimatedLossRate == nil || *stats.EstimatedLossRate != 100 {
		t.Fatalf("all failed probes must produce 100%% loss, got %v", stats.EstimatedLossRate)
	}
}

func TestEstimatedLatencySpikeCountsAsLossAndRetainsMeasurementStats(t *testing.T) {
	latency100, latency500 := 100.0, 500.0
	stats := calculateStats([]Sample{
		{Status: StatusSuccess, Latency: &latency100},
		{Status: StatusPacketLoss, LossReason: LossReasonLatencySpike, Latency: &latency500},
	})
	if stats.EstimatedLossRate == nil || *stats.EstimatedLossRate != 50 || stats.SuccessRate != 50 {
		t.Fatalf("spike was not counted as one estimated loss: %#v", stats)
	}
	if stats.CurrentMS == nil || *stats.CurrentMS != latency500 || stats.P95MS == nil || *stats.P95MS != latency500 {
		t.Fatalf("spike measurement was omitted from current/P95 stats: %#v", stats)
	}
}

func TestRingKeepsOnlyLatestErrorMessage(t *testing.T) {
	ring := newSampleRing(4)
	ring.Add(Sample{Status: StatusTimeout, Message: "first"})
	ring.Add(Sample{Status: StatusSuccess})
	ring.Add(Sample{Status: StatusRefused, Message: "latest"})
	values := ring.Values()
	if values[0].Message != "" || values[2].Message != "latest" {
		t.Fatalf("unexpected retained messages: %#v", values)
	}
	ring.Add(Sample{Status: StatusOther})
	values = ring.Values()
	for _, sample := range values {
		if sample.Message != "" {
			t.Fatalf("an older error message survived a newer error: %#v", values)
		}
	}
}

func TestP95(t *testing.T) {
	samples := make([]Sample, 20)
	for index := range samples {
		latency := float64(index + 1)
		samples[index] = Sample{Status: StatusSuccess, Latency: &latency}
	}
	stats := calculateStats(samples)
	if stats.P95MS == nil || *stats.P95MS != 19 {
		t.Fatalf("expected nearest-rank P95 of 19, got %#v", stats.P95MS)
	}
	if stats.CurrentMS == nil || *stats.CurrentMS != 20 {
		t.Fatalf("expected current latency 20, got %#v", stats.CurrentMS)
	}
}

func TestNodePhaseStatsAndTimeoutRates(t *testing.T) {
	localOne, tunnelOne, tlsOne, googleOne := 1.0, 42.0, 81.0, 96.0
	localTwo := 1.2
	localThree, tunnelThree := 0.9, 38.0
	samples := []Sample{
		{ProbeKind: ProbeKindProxyGoogle, Status: StatusSuccess, Stage: StageHTTP, Latency: &googleOne, LocalProxy: &localOne, Tunnel: &tunnelOne, TLS: &tlsOne, Google: &googleOne},
		{ProbeKind: ProbeKindProxyGoogle, Status: StatusTimeout, Stage: StageSOCKS, LocalProxy: &localTwo},
		{ProbeKind: ProbeKindProxyGoogle, Status: StatusTimeout, Stage: StageTLS, LocalProxy: &localThree, Tunnel: &tunnelThree},
		{ProbeKind: ProbeKindProxyGoogle, Status: StatusLocalProxyRefused, Stage: StageLocalProxy},
	}
	stats := calculateStats(samples)
	if stats.TunnelTimeoutRate == nil || *stats.TunnelTimeoutRate != 33.33 {
		t.Fatalf("unexpected tunnel timeout rate: %#v", stats.TunnelTimeoutRate)
	}
	if stats.GoogleTimeoutRate == nil || *stats.GoogleTimeoutRate != 75 {
		t.Fatalf("unexpected Google timeout rate: %#v", stats.GoogleTimeoutRate)
	}
	if stats.SuccessRate != 25 || stats.TimeoutRate != 50 {
		t.Fatalf("unexpected node base rates: %#v", stats)
	}
	if stats.LocalProxyCurrentMS != nil || stats.TunnelCurrentMS != nil || stats.GoogleCurrentMS != nil {
		t.Fatalf("last local proxy failure should clear current phase values: %#v", stats)
	}
}
