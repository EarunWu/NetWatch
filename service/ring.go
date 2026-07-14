package main

import (
	"math"
	"sort"
)

type sampleRing struct {
	items        []Sample
	start        int
	count        int
	messageIndex int
}

func newSampleRing(capacity int) *sampleRing {
	if capacity < 1 {
		capacity = 1
	}
	return &sampleRing{items: make([]Sample, capacity), messageIndex: -1}
}

func (r *sampleRing) Add(sample Sample) {
	var index int
	if r.count < len(r.items) {
		index = (r.start + r.count) % len(r.items)
		r.count++
	} else {
		index = r.start
		r.start = (r.start + 1) % len(r.items)
	}
	if index == r.messageIndex {
		r.messageIndex = -1
	}
	if sample.Status != StatusSuccess {
		if r.messageIndex >= 0 {
			r.items[r.messageIndex].Message = ""
		}
		r.messageIndex = -1
		if sample.Message != "" {
			r.messageIndex = index
		}
	}
	r.items[index] = sample
}

func (r *sampleRing) Values() []Sample {
	values := make([]Sample, r.count)
	for i := 0; i < r.count; i++ {
		values[i] = r.items[(r.start+i)%len(r.items)]
	}
	return values
}

func (r *sampleRing) Load(values []Sample) {
	if len(values) > len(r.items) {
		values = values[len(values)-len(r.items):]
	}
	for _, sample := range values {
		r.Add(sample)
	}
}

func (r *sampleRing) At(index int) Sample {
	return r.items[(r.start+index)%len(r.items)]
}

type statsBuilder struct {
	total              int
	success            int
	timeout            int
	refused            int
	nodeSamples        int
	tunnelSuccess      int
	tunnelTimeout      int
	consecutiveFailure int
	last               Sample
	latencies          []float64
}

func newStatsBuilder(scratch []float64) statsBuilder {
	return statsBuilder{latencies: scratch[:0]}
}

func (b *statsBuilder) Add(sample Sample) {
	b.total++
	b.last = sample
	if sample.Latency != nil && validMeasuredLatency(*sample.Latency) {
		b.latencies = append(b.latencies, *sample.Latency)
	}
	if sample.ProbeKind == ProbeKindProxyGoogle {
		b.nodeSamples++
		if sample.Tunnel != nil {
			b.tunnelSuccess++
		} else if sample.Status == StatusTimeout && sample.Stage == StageSOCKS {
			b.tunnelTimeout++
		}
	}
	switch sample.Status {
	case StatusSuccess:
		b.success++
		b.consecutiveFailure = 0
	case StatusTimeout:
		b.timeout++
		b.consecutiveFailure++
	case StatusRefused:
		b.refused++
		b.consecutiveFailure++
	default:
		b.consecutiveFailure++
	}
}

func (b *statsBuilder) Finish() Stats {
	stats := Stats{SampleCount: b.total}
	if b.total == 0 {
		return stats
	}
	total := float64(b.total)
	stats.SuccessRate = percent(b.success, total)
	stats.TimeoutRate = percent(b.timeout, total)
	stats.RefusedRate = percent(b.refused, total)
	// Every final non-success result, including a completed latency spike, is one
	// estimated loss. A spike may still carry a measured latency for charts and
	// distribution statistics.
	stats.EstimatedLossRate = floatPointer(percent(b.total-b.success, total))
	if b.nodeSamples > 0 {
		tunnelAttempts := float64(b.tunnelSuccess + b.tunnelTimeout)
		if tunnelAttempts > 0 {
			stats.TunnelTimeoutRate = floatPointer(percent(b.tunnelTimeout, tunnelAttempts))
		}
		if stats.EstimatedLossRate != nil {
			stats.GoogleTimeoutRate = floatPointer(*stats.EstimatedLossRate)
		}
	}

	stats.LastStatus = b.last.Status
	stats.LastSampleAt = b.last.TS
	stats.ConsecutiveFailure = b.consecutiveFailure
	if b.last.Latency != nil && validMeasuredLatency(*b.last.Latency) {
		stats.CurrentMS = floatPointer(*b.last.Latency)
	}
	stats.LocalProxyCurrentMS = copyFloatPointer(b.last.LocalProxy)
	stats.TunnelCurrentMS = copyFloatPointer(b.last.Tunnel)
	stats.RemoteFirstByteCurrentMS = copyFloatPointer(b.last.RemoteFirstByte)
	stats.TLSCurrentMS = copyFloatPointer(b.last.TLS)
	stats.GoogleCurrentMS = copyFloatPointer(b.last.Google)

	if len(b.latencies) > 0 {
		sort.Float64s(b.latencies)
		index := int(math.Ceil(float64(len(b.latencies))*0.95)) - 1
		stats.P95MS = floatPointer(b.latencies[index])
	}
	return stats
}

func calculateStats(samples []Sample) Stats {
	builder := newStatsBuilder(make([]float64, 0, len(samples)))
	for _, sample := range samples {
		builder.Add(sample)
	}
	return builder.Finish()
}

func calculateRingStats(ring *sampleRing, scratch []float64) (Stats, []float64) {
	builder := newStatsBuilder(scratch)
	for index := 0; index < ring.count; index++ {
		builder.Add(ring.At(index))
	}
	return builder.Finish(), builder.latencies
}

func percent(count int, total float64) float64 {
	if total == 0 {
		return 0
	}
	return math.Round((float64(count)/total*100)*100) / 100
}

func floatPointer(value float64) *float64 {
	copy := value
	return &copy
}

func copyFloatPointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	return floatPointer(*value)
}
