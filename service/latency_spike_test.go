package main

import (
	"math"
	"strings"
	"testing"
)

func successfulDirectSample(latency float64) Sample {
	value := latency
	return Sample{
		ProbeKind: ProbeKindDirectTCP,
		Latency:   &value,
		Stage:     StageTCP,
		Status:    StatusSuccess,
	}
}

func successfulNodeSample(tlsLatency, terminalLatency float64, google204 bool) (Target, Sample) {
	tlsValue := tlsLatency
	terminalValue := terminalLatency
	sample := Sample{
		ProbeKind: ProbeKindProxyGoogle,
		Latency:   &terminalValue,
		TLS:       &tlsValue,
		Stage:     StageTLS,
		Status:    StatusSuccess,
	}
	target := Target{ID: "node", Kind: ProbeKindProxyGoogle, Google204Enabled: google204}
	if google204 {
		googleValue := terminalLatency
		sample.Google = &googleValue
		sample.Stage = StageHTTP
	}
	return target, sample
}

func seedBaseline(baseline *latencyBaseline, value float64, count int) {
	for index := 0; index < count; index++ {
		baseline.add(value)
	}
}

func TestLatencySpikeWarmsUpBeforeClassifying(t *testing.T) {
	var baseline latencyBaseline
	target := Target{ID: "direct", Kind: ProbeKindDirectTCP}
	seedBaseline(&baseline, 170, latencyBaselineMinimum-1)

	sample := baseline.classify(target, successfulDirectSample(500))
	if sample.Status != StatusSuccess || sample.LossReason != "" {
		t.Fatalf("warm-up sample was classified as loss: %#v", sample)
	}
	if baseline.count != latencyBaselineMinimum {
		t.Fatalf("warm-up measurement was not retained: %d", baseline.count)
	}
}

func TestLatencySpikeUsesStrictAdaptiveThreshold(t *testing.T) {
	target := Target{ID: "direct", Kind: ProbeKindDirectTCP}
	for _, test := range []struct {
		name       string
		latency    float64
		wantStatus string
	}{
		{name: "ordinary", latency: 369.999, wantStatus: StatusSuccess},
		{name: "boundary", latency: 370, wantStatus: StatusSuccess},
		{name: "spike", latency: 500, wantStatus: StatusPacketLoss},
	} {
		t.Run(test.name, func(t *testing.T) {
			var baseline latencyBaseline
			seedBaseline(&baseline, 170, latencyBaselineMinimum)
			sample := baseline.classify(target, successfulDirectSample(test.latency))
			if sample.Status != test.wantStatus {
				t.Fatalf("unexpected classification at %.3f ms: %#v", test.latency, sample)
			}
			if sample.Latency == nil || *sample.Latency != test.latency {
				t.Fatalf("completed measurement was discarded: %#v", sample)
			}
			if test.wantStatus == StatusPacketLoss {
				if sample.LossReason != LossReasonLatencySpike || sample.Stage != StageTCP {
					t.Fatalf("spike metadata missing: %#v", sample)
				}
				if !strings.Contains(sample.Message, "170.000 ms") || !strings.Contains(sample.Message, "370.000 ms") {
					t.Fatalf("spike diagnostics omitted the baseline or threshold: %q", sample.Message)
				}
			}
		})
	}
}

func TestLatencySpikeUsesMultiplierForSlowerBaseline(t *testing.T) {
	target := Target{ID: "direct", Kind: ProbeKindDirectTCP}
	for _, test := range []struct {
		latency    float64
		wantStatus string
	}{
		{latency: 600, wantStatus: StatusSuccess},
		{latency: 600.001, wantStatus: StatusPacketLoss},
	} {
		var baseline latencyBaseline
		seedBaseline(&baseline, 300, latencyBaselineMinimum)
		sample := baseline.classify(target, successfulDirectSample(test.latency))
		if sample.Status != test.wantStatus {
			t.Fatalf("unexpected classification at %.3f ms: %#v", test.latency, sample)
		}
	}
}

func TestLatencySpikeUsesTLSForNodeEvenWhenGoogle204IsEnabled(t *testing.T) {
	var baseline latencyBaseline
	seedBaseline(&baseline, 170, latencyBaselineMinimum)
	target, slowHTTP := successfulNodeSample(180, 900, true)
	if sample := baseline.classify(target, slowHTTP); sample.Status != StatusSuccess {
		t.Fatalf("HTTP-only delay was mistaken for a TLS spike: %#v", sample)
	}

	var spikeBaseline latencyBaseline
	seedBaseline(&spikeBaseline, 170, latencyBaselineMinimum)
	_, tlsSpike := successfulNodeSample(500, 520, true)
	sample := spikeBaseline.classify(target, tlsSpike)
	if sample.Status != StatusPacketLoss || sample.LossReason != LossReasonLatencySpike || sample.Stage != StageTLS {
		t.Fatalf("TLS spike was not classified: %#v", sample)
	}
	if sample.TLS == nil || *sample.TLS != 500 || sample.Latency == nil || *sample.Latency != 520 || sample.Google == nil {
		t.Fatalf("node spike discarded completed phase measurements: %#v", sample)
	}
}

func TestLatencySpikeWindowKeepsLatestThirtyMeasurements(t *testing.T) {
	var baseline latencyBaseline
	for index := 1; index <= latencyBaselineCapacity+5; index++ {
		baseline.add(float64(index))
	}
	if baseline.count != latencyBaselineCapacity {
		t.Fatalf("unexpected window size: %d", baseline.count)
	}
	median, ready := baseline.median()
	if !ready || median != 20.5 {
		t.Fatalf("rolling median did not discard the oldest values: %.3f ready=%v", median, ready)
	}
}

func TestLatencySpikeMedianSupportsOddAndEvenWindows(t *testing.T) {
	var even latencyBaseline
	for _, value := range []float64{1, 9, 2, 8, 3, 7, 4, 6, 5, 10} {
		even.add(value)
	}
	if median, ready := even.median(); !ready || median != 5.5 {
		t.Fatalf("unexpected even median: %.3f ready=%v", median, ready)
	}

	var odd latencyBaseline
	for _, value := range []float64{1, 11, 2, 10, 3, 9, 4, 8, 5, 7, 6} {
		odd.add(value)
	}
	if median, ready := odd.median(); !ready || median != 6 {
		t.Fatalf("unexpected odd median: %.3f ready=%v", median, ready)
	}
}

func TestLatencySpikeIgnoresFailuresAndInvalidMeasurements(t *testing.T) {
	var baseline latencyBaseline
	seedBaseline(&baseline, 170, latencyBaselineMinimum)
	before := baseline.count

	timeout := baseline.classify(Target{Kind: ProbeKindDirectTCP}, Sample{Status: StatusTimeout, Stage: StageTCP})
	if timeout.Status != StatusTimeout || baseline.count != before {
		t.Fatalf("timeout was changed or added to the baseline: %#v count=%d", timeout, baseline.count)
	}
	for _, invalid := range []float64{-1, math.NaN(), math.Inf(1)} {
		sample := baseline.classify(Target{Kind: ProbeKindDirectTCP}, successfulDirectSample(invalid))
		if sample.Status != StatusSuccess || baseline.count != before {
			t.Fatalf("invalid latency polluted the baseline: %#v count=%d", sample, baseline.count)
		}
	}
}

func TestLatencySpikesParticipateInRouteAdaptation(t *testing.T) {
	var baseline latencyBaseline
	seedBaseline(&baseline, 170, latencyBaselineCapacity)
	target := Target{Kind: ProbeKindDirectTCP}

	classified := 0
	for index := 0; index < latencyBaselineCapacity; index++ {
		sample := baseline.classify(target, successfulDirectSample(500))
		if sample.Status == StatusPacketLoss {
			classified++
		}
	}
	if classified == 0 || classified >= latencyBaselineCapacity {
		t.Fatalf("new stable route did not replace the old baseline: %d spikes", classified)
	}
	median, ready := baseline.median()
	if !ready || median != 500 {
		t.Fatalf("new route was not retained as the baseline: %.3f ready=%v", median, ready)
	}
}

func TestLatencyBaselineLoadIncludesCompletedSpikes(t *testing.T) {
	target := Target{Kind: ProbeKindDirectTCP}
	history := make([]Sample, 0, latencyBaselineCapacity+4)
	for index := 0; index < latencyBaselineCapacity; index++ {
		history = append(history, successfulDirectSample(170))
	}
	for index := 0; index < 4; index++ {
		sample := successfulDirectSample(500)
		sample.Status = StatusPacketLoss
		sample.LossReason = LossReasonLatencySpike
		history = append(history, sample)
	}

	var baseline latencyBaseline
	baseline.load(target, history)
	if baseline.count != latencyBaselineCapacity {
		t.Fatalf("loaded baseline has the wrong size: %d", baseline.count)
	}
	if median, ready := baseline.median(); !ready || median != 170 {
		t.Fatalf("loaded baseline has the wrong median: %.3f ready=%v", median, ready)
	}
}

func TestTargetRuntimesKeepIndependentBaselines(t *testing.T) {
	fast := newTargetRuntime(Target{ID: "fast", Kind: ProbeKindDirectTCP}, 2, nil, nil)
	slow := newTargetRuntime(Target{ID: "slow", Kind: ProbeKindDirectTCP}, 2, nil, nil)
	for index := 0; index < latencyBaselineMinimum; index++ {
		_, _, _ = fast.add(successfulDirectSample(50), false)
		_, _, _ = slow.add(successfulDirectSample(500), false)
	}

	fastSample, _, _ := fast.add(successfulDirectSample(300), false)
	slowSample, _, _ := slow.add(successfulDirectSample(700), false)
	if fastSample.Status != StatusPacketLoss {
		t.Fatalf("fast target did not use its own baseline: %#v", fastSample)
	}
	if slowSample.Status != StatusSuccess {
		t.Fatalf("slow target was classified using another target's baseline: %#v", slowSample)
	}
}
