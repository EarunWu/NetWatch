package main

import (
	"fmt"
	"math"
	"sort"
)

const (
	latencyBaselineCapacity     = 30
	latencyBaselineMinimum      = 10
	latencySpikeMultiplier      = 2.0
	latencySpikeMinimumIncrease = 200.0
)

// latencyBaseline keeps a small target-local window of completed measurements.
// Estimated-loss samples stay in the window so a real route or node switch can
// establish a new baseline instead of being classified as a spike forever.
type latencyBaseline struct {
	values [latencyBaselineCapacity]float64
	start  int
	count  int
}

func (b *latencyBaseline) add(value float64) {
	if !validMeasuredLatency(value) {
		return
	}
	if b.count < len(b.values) {
		b.values[(b.start+b.count)%len(b.values)] = value
		b.count++
		return
	}
	b.values[b.start] = value
	b.start = (b.start + 1) % len(b.values)
}

func (b *latencyBaseline) median() (float64, bool) {
	if b.count < latencyBaselineMinimum {
		return 0, false
	}
	var ordered [latencyBaselineCapacity]float64
	for index := 0; index < b.count; index++ {
		ordered[index] = b.values[(b.start+index)%len(b.values)]
	}
	values := ordered[:b.count]
	sort.Float64s(values)
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return values[middle], true
	}
	return (values[middle-1] + values[middle]) / 2, true
}

func (b *latencyBaseline) load(target Target, samples []Sample) {
	for _, sample := range samples {
		if value, ok := baselineMeasurement(target, sample); ok {
			b.add(value)
		}
	}
}

func (b *latencyBaseline) classify(target Target, sample Sample) Sample {
	value, measured := currentMeasurement(target, sample)
	if !measured || sample.Status != StatusSuccess {
		return sample
	}

	baseline, ready := b.median()
	if ready {
		threshold := math.Max(
			baseline*latencySpikeMultiplier,
			baseline+latencySpikeMinimumIncrease,
		)
		if value > threshold {
			sample.Status = StatusPacketLoss
			sample.LossReason = LossReasonLatencySpike
			metric := "TCP 建连"
			sample.Stage = StageTCP
			if target.Kind == ProbeKindProxyGoogle {
				metric = "TLS 完成"
				sample.Stage = StageTLS
			}
			sample.Message = truncateMessage(fmt.Sprintf(
				"%s延迟尖峰：本次 %.3f ms，滚动基准 %.3f ms，判定阈值 %.3f ms",
				metric,
				value,
				baseline,
				threshold,
			), 240)
		}
	}

	// Add after classification so the current result never influences its own
	// threshold. A spike still represents a completed measurement and therefore
	// participates in future route adaptation.
	b.add(value)
	return sample
}

func baselineMeasurement(target Target, sample Sample) (float64, bool) {
	if sample.Status != StatusSuccess &&
		!(sample.Status == StatusPacketLoss && sample.LossReason == LossReasonLatencySpike) {
		return 0, false
	}
	return currentMeasurement(target, sample)
}

func currentMeasurement(target Target, sample Sample) (float64, bool) {
	value := sample.Latency
	if target.Kind == ProbeKindProxyGoogle {
		value = sample.TLS
	}
	if value == nil || !validMeasuredLatency(*value) {
		return 0, false
	}
	return *value, true
}

func validMeasuredLatency(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
