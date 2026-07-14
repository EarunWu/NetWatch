package main

import (
	"math"
	"testing"
	"time"
)

func TestChartHistoryAggregatesSuccessAndKeepsTimeout(t *testing.T) {
	history := newChartHistory(3)
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC).UnixMilli()
	latency10 := 10.0
	latency30 := 30.0
	tls20 := 20.0
	tls40 := 40.0
	history.Add(Sample{TS: base + 5_000, Status: StatusSuccess, Latency: &latency10, TLS: &tls20})
	history.Add(Sample{TS: base + 40_000, Status: StatusSuccess, Latency: &latency30, TLS: &tls40})
	history.Add(Sample{TS: base + 50_000, Status: StatusTimeout, Stage: StageTLS})

	target := Target{ID: "node", Kind: ProbeKindProxyGoogle}
	samples := history.SamplesBefore(target, math.MaxInt64)
	if len(samples) != 2 {
		t.Fatalf("expected aggregate plus timeout, got %#v", samples)
	}
	if samples[0].Status != StatusSuccess || samples[0].Latency == nil || *samples[0].Latency != 20 || samples[0].TLS == nil || *samples[0].TLS != 30 {
		t.Fatalf("unexpected aggregate: %#v", samples[0])
	}
	if samples[0].BucketMS != chartHistoryBucketMS || samples[0].Stage != StageTLS {
		t.Fatalf("aggregate metadata missing: %#v", samples[0])
	}
	if samples[1].Status != StatusTimeout || samples[1].Stage != StageTLS || samples[1].TS != base+50_000 {
		t.Fatalf("timeout marker was not retained: %#v", samples[1])
	}
}

func TestChartHistoryKeepsLatestNonSuccessMarker(t *testing.T) {
	statuses := []string{
		StatusTimeout,
		StatusRefused,
		StatusDNSError,
		StatusNoRoute,
		StatusLocalProxyRefused,
		StatusLocalProxyTimeout,
		StatusLocalProxyError,
		StatusSOCKSAuthFailed,
		StatusSOCKSRejected,
		StatusSOCKSProtocol,
		StatusTLSError,
		StatusTLSCertificate,
		StatusHTTPError,
		StatusUnexpectedHTTP,
		StatusPacketLoss,
		StatusOther,
	}
	history := newChartHistory(len(statuses))
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC).UnixMilli()
	for index, status := range statuses {
		ts := base + int64(index)*chartHistoryBucketMS + 1_000
		history.Add(Sample{TS: ts, Status: StatusTimeout, Stage: StageSOCKS})
		// The later failure is the representative marker for this minute.
		history.Add(Sample{TS: ts + 1_000, Status: status, Stage: StageTLS})
	}

	samples := history.SamplesBefore(Target{ID: "node", Kind: ProbeKindProxyGoogle}, math.MaxInt64)
	if len(samples) != len(statuses) {
		t.Fatalf("expected one compact failure per minute, got %d: %#v", len(samples), samples)
	}
	for index, sample := range samples {
		if sample.Status != statuses[index] || sample.Stage != StageTLS || sample.TS != base+int64(index)*chartHistoryBucketMS+2_000 {
			t.Fatalf("failure %d lost its status/stage/timestamp: %#v", index, sample)
		}
	}
}

func TestChartHistorySummariesAggregateCountsMomentsAndHistogram(t *testing.T) {
	history := newChartHistory(2)
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC).UnixMilli()
	latency100, latency200 := 100.0, 200.0
	tls110, tls220, tls330 := 110.0, 220.0, 330.0
	tunnel20, tunnel30, tunnel40 := 20.0, 30.0, 40.0

	history.Add(Sample{TS: base + 1_000, ProbeKind: ProbeKindProxyGoogle, Status: StatusSuccess, Latency: &latency100, TLS: &tls110, Tunnel: &tunnel20})
	history.Add(Sample{TS: base + 2_000, ProbeKind: ProbeKindProxyGoogle, Status: StatusSuccess, Latency: &latency200, TLS: &tls220, Tunnel: &tunnel30})
	history.Add(Sample{TS: base + 3_000, ProbeKind: ProbeKindProxyGoogle, Status: StatusTimeout, Stage: StageSOCKS})
	history.Add(Sample{TS: base + 4_000, ProbeKind: ProbeKindProxyGoogle, Status: StatusRefused})
	// TLS completed even though the optional HTTP stage failed.
	history.Add(Sample{TS: base + 5_000, ProbeKind: ProbeKindProxyGoogle, Status: StatusHTTPError, Stage: StageHTTP, TLS: &tls330, Tunnel: &tunnel40})

	summaries := history.SummariesBefore(base + chartHistoryBucketMS)
	if len(summaries) != 1 {
		t.Fatalf("expected one completed summary, got %#v", summaries)
	}
	summary := summaries[0]
	if summary.StartMS != base || summary.DurationMS != chartHistoryBucketMS {
		t.Fatalf("unexpected summary bounds: %#v", summary)
	}
	if summary.TotalCount != 5 || summary.SuccessCount != 2 || summary.TimeoutCount != 1 || summary.RefusedCount != 1 {
		t.Fatalf("unexpected status counts: %#v", summary)
	}
	if summary.TunnelSuccessCount != 3 || summary.TunnelTimeoutCount != 1 {
		t.Fatalf("unexpected tunnel counts: %#v", summary)
	}
	if summary.LatencyCount != 2 || summary.LatencySum != 300 || summary.LatencySumSquares != 50_000 {
		t.Fatalf("unexpected latency moments: %#v", summary)
	}
	if summary.TLSCount != 3 || summary.TLSSum != 660 || summary.TLSSumSquares != 169_400 {
		t.Fatalf("unexpected TLS moments: %#v", summary)
	}
	if len(summary.LatencyHistogram) != 2 {
		t.Fatalf("expected two sparse histogram bins, got %#v", summary.LatencyHistogram)
	}
	for index, original := range []float64{latency100, latency200} {
		bin := summary.LatencyHistogram[index]
		if bin.Count != 1 {
			t.Fatalf("unexpected histogram count: %#v", bin)
		}
		relativeError := math.Abs(bin.ValueMS-original) / original
		if relativeError > latencyHistogramRelativeAccuracy+1e-12 {
			t.Fatalf("histogram representative %.6f is %.4f%% from %.6f", bin.ValueMS, relativeError*100, original)
		}
	}
}

func TestChartHistorySummaryRequiresCompleteBucketBeforeCutoff(t *testing.T) {
	history := newChartHistory(2)
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC).UnixMilli()
	latency := 12.0
	history.Add(Sample{TS: base + 5_000, Status: StatusSuccess, Latency: &latency})

	if summaries := history.SummariesBefore(base + chartHistoryBucketMS - 1); len(summaries) != 0 {
		t.Fatalf("straddling bucket must be excluded, got %#v", summaries)
	}
	if summaries := history.SummariesBefore(base + chartHistoryBucketMS); len(summaries) != 1 {
		t.Fatalf("bucket ending exactly at cutoff must be included, got %#v", summaries)
	}
}

func TestSparseLatencyHistogramRelativeErrorAndAllocation(t *testing.T) {
	history := newChartHistory(2)
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC).UnixMilli()
	history.Add(Sample{TS: base + 1_000, Status: StatusTimeout})
	if history.items[0].latencyHistogram != nil {
		t.Fatal("histogram map was allocated without a successful latency")
	}

	inputs := []float64{0, 0.1, 1, 10, 123.456, 123.456, 59_999}
	for index := range inputs {
		value := inputs[index]
		history.Add(Sample{TS: base + int64(index+2)*1_000, Status: StatusSuccess, Latency: &value})
	}
	summaries := history.SummariesBefore(base + chartHistoryBucketMS)
	if len(summaries) != 1 {
		t.Fatalf("expected one histogram summary, got %#v", summaries)
	}
	histogram := summaries[0].LatencyHistogram
	var count uint32
	foundMergedBin := false
	for _, bin := range histogram {
		count += bin.Count
		if bin.Count > 1 {
			foundMergedBin = true
		}
	}
	if count != uint32(len(inputs)) {
		t.Fatalf("histogram lost observations: got %d want %d", count, len(inputs))
	}
	if !foundMergedBin {
		t.Fatalf("duplicate observations were not combined: %#v", histogram)
	}
	for _, original := range inputs {
		matched := false
		for _, bin := range histogram {
			if original == 0 {
				matched = bin.ValueMS == 0
			} else {
				matched = math.Abs(bin.ValueMS-original)/original <= latencyHistogramRelativeAccuracy+1e-12
			}
			if matched {
				break
			}
		}
		if !matched {
			t.Fatalf("no representative within %.1f%% for %.6f: %#v", latencyHistogramRelativeAccuracy*100, original, histogram)
		}
	}
}

func TestRuntimeSnapshotSeparatesRawAndChartHistory(t *testing.T) {
	target := Target{ID: "direct", Kind: ProbeKindDirectTCP}
	runtime := newTargetRuntime(target, 2, nil, nil)
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC).UnixMilli()
	for index, latency := range []float64{10, 20, 30} {
		value := latency
		_, _, _ = runtime.add(Sample{
			TargetID: target.ID,
			TS:       base + int64(index)*chartHistoryBucketMS + 1_000,
			Status:   StatusSuccess,
			Latency:  &value,
		}, false)
	}

	snapshot := runtime.snapshot()
	if len(snapshot.Samples) != 2 || len(snapshot.ChartSamples) != 1 || len(snapshot.ChartBuckets) != 1 {
		t.Fatalf(
			"unexpected snapshot history sizes: raw=%d chart=%d buckets=%d",
			len(snapshot.Samples),
			len(snapshot.ChartSamples),
			len(snapshot.ChartBuckets),
		)
	}
	if snapshot.ChartSamples[0].TS != base+1_000 || snapshot.ChartSamples[0].BucketMS != chartHistoryBucketMS {
		t.Fatalf("unexpected historical chart sample: %#v", snapshot.ChartSamples[0])
	}
	if snapshot.ChartBuckets[0].StartMS != base || snapshot.ChartBuckets[0].LatencyCount != 1 {
		t.Fatalf("unexpected historical chart bucket: %#v", snapshot.ChartBuckets[0])
	}
}

func TestChartHistoryRetainsLatencySpikeMeasurementAndReason(t *testing.T) {
	history := newChartHistory(2)
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC).UnixMilli()
	latency, tls := 520.0, 500.0
	history.Add(Sample{
		TS:         base + 5_000,
		ProbeKind:  ProbeKindProxyGoogle,
		Status:     StatusPacketLoss,
		LossReason: LossReasonLatencySpike,
		Stage:      StageTLS,
		Latency:    &latency,
		TLS:        &tls,
	})

	summaries := history.SummariesBefore(base + chartHistoryBucketMS)
	if len(summaries) != 1 {
		t.Fatalf("expected one summary, got %#v", summaries)
	}
	summary := summaries[0]
	if summary.TotalCount != 1 || summary.SuccessCount != 0 {
		t.Fatalf("spike was not counted as one loss: %#v", summary)
	}
	if summary.LatencyCount != 1 || summary.LatencySum != latency || summary.TLSCount != 1 || summary.TLSSum != tls {
		t.Fatalf("spike measurement was omitted from latency aggregates: %#v", summary)
	}
	if len(summary.LatencyHistogram) != 1 || summary.LatencyHistogram[0].Count != 1 {
		t.Fatalf("spike measurement was omitted from the histogram: %#v", summary.LatencyHistogram)
	}

	samples := history.SamplesBefore(Target{ID: "node", Kind: ProbeKindProxyGoogle}, math.MaxInt64)
	if len(samples) != 2 {
		t.Fatalf("expected minute measurement and spike marker, got %#v", samples)
	}
	var marker *Sample
	for index := range samples {
		if samples[index].Status == StatusPacketLoss {
			marker = &samples[index]
		}
	}
	if marker == nil || marker.LossReason != LossReasonLatencySpike || marker.Latency == nil || *marker.Latency != latency || marker.TLS == nil || *marker.TLS != tls {
		t.Fatalf("historical spike marker lost its value or reason: %#v", marker)
	}
}
