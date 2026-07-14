package main

import (
	"math"
	"sort"
)

const (
	chartHistoryBucketMS = int64(60 * 1000)
	chartHistoryWindowMS = int64(12 * 60 * 60 * 1000)
	chartHistoryCapacity = int(chartHistoryWindowMS/chartHistoryBucketMS) + 1

	latencyHistogramRelativeAccuracy       = 0.02
	latencyHistogramGamma                  = (1 + latencyHistogramRelativeAccuracy) / (1 - latencyHistogramRelativeAccuracy)
	latencyHistogramZeroKey          int32 = -1 << 31
)

var latencyHistogramLogMultiplier = 1 / math.Log(latencyHistogramGamma)

const (
	chartMetricLatency = iota
	chartMetricLocalProxy
	chartMetricTunnel
	chartMetricRemoteFirstByte
	chartMetricTLS
	chartMetricGoogle
	chartMetricCount
)

const (
	chartStageUnknown byte = iota
	chartStageTCP
	chartStageLocalProxy
	chartStageSOCKS
	chartStageTLS
	chartStageHTTP
)

const (
	chartStatusUnknown byte = iota
	chartStatusTimeout
	chartStatusRefused
	chartStatusDNSError
	chartStatusNoRoute
	chartStatusLocalProxyRefused
	chartStatusLocalProxyTimeout
	chartStatusLocalProxyError
	chartStatusSOCKSAuthFailed
	chartStatusSOCKSRejected
	chartStatusSOCKSProtocol
	chartStatusTLSError
	chartStatusTLSCertificate
	chartStatusHTTPError
	chartStatusUnexpectedHTTP
	chartStatusPacketLoss
	chartStatusOther
)

const (
	chartLossReasonUnknown byte = iota
	chartLossReasonLatencySpike
)

type chartMetricAggregate struct {
	sum        float64
	sumSquares float64
	count      uint32
}

func (a *chartMetricAggregate) add(value *float64) {
	if value == nil {
		return
	}
	a.sum += *value
	a.sumSquares += *value * *value
	a.count++
}

func (a chartMetricAggregate) average() *float64 {
	if a.count == 0 {
		return nil
	}
	return floatPointer(a.sum / float64(a.count))
}

type chartHistoryBucket struct {
	startMS             int64
	latestMeasurementTS int64
	latestFailureTS     int64
	metrics             [chartMetricCount]chartMetricAggregate
	totalCount          uint32
	successCount        uint32
	timeoutCount        uint32
	refusedCount        uint32
	tunnelSuccessCount  uint32
	tunnelTimeoutCount  uint32
	latencyHistogram    map[int32]uint32
	failureStage        byte
	failureStatus       byte
	failureLossReason   byte
	failureLatency      float64
	failureLatencySet   bool
	failureTLS          float64
	failureTLSSet       bool
}

type chartHistory struct {
	items []chartHistoryBucket
	start int
	count int
}

func newChartHistory(capacity int) *chartHistory {
	if capacity < 1 {
		capacity = 1
	}
	return &chartHistory{items: make([]chartHistoryBucket, capacity)}
}

func (h *chartHistory) Add(sample Sample) {
	if sample.TS <= 0 {
		return
	}
	bucketStart := sample.TS - sample.TS%chartHistoryBucketMS
	bucket := h.latestBucket(bucketStart)
	if bucket == nil {
		bucket = h.appendBucket(chartHistoryBucket{startMS: bucketStart})
	}
	bucket.totalCount++
	if sample.Tunnel != nil {
		bucket.tunnelSuccessCount++
	} else if sample.Status == StatusTimeout && sample.Stage == StageSOCKS {
		bucket.tunnelTimeoutCount++
	}
	if sample.Status == StatusSuccess {
		bucket.successCount++
	}
	if sample.Latency != nil && validMeasuredLatency(*sample.Latency) {
		bucket.latestMeasurementTS = sample.TS
		bucket.metrics[chartMetricLatency].add(sample.Latency)
		bucket.metrics[chartMetricLocalProxy].add(sample.LocalProxy)
		bucket.metrics[chartMetricTunnel].add(sample.Tunnel)
		bucket.metrics[chartMetricRemoteFirstByte].add(sample.RemoteFirstByte)
		bucket.metrics[chartMetricGoogle].add(sample.Google)
		bucket.addLatencyHistogram(sample.Latency)
	}
	if sample.TLS != nil && validMeasuredLatency(*sample.TLS) {
		bucket.latestMeasurementTS = sample.TS
	}
	if sample.Status == StatusTimeout {
		bucket.timeoutCount++
	}
	if sample.Status == StatusRefused {
		bucket.refusedCount++
	}
	// A Google-204 probe can complete TLS and then fail during HTTP. Such a
	// sample still contributes a valid TLS-completion latency.
	bucket.metrics[chartMetricTLS].add(sample.TLS)
	if sample.Status != StatusSuccess {
		bucket.latestFailureTS = sample.TS
		bucket.failureStage = encodeChartStage(sample.Stage)
		bucket.failureStatus = encodeChartStatus(sample.Status)
		bucket.failureLossReason = encodeChartLossReason(sample.LossReason)
		bucket.failureLatencySet = sample.Latency != nil && validMeasuredLatency(*sample.Latency)
		if bucket.failureLatencySet {
			bucket.failureLatency = *sample.Latency
		} else {
			bucket.failureLatency = 0
		}
		bucket.failureTLSSet = sample.TLS != nil && validMeasuredLatency(*sample.TLS)
		if bucket.failureTLSSet {
			bucket.failureTLS = *sample.TLS
		} else {
			bucket.failureTLS = 0
		}
	}
}

func (b *chartHistoryBucket) addLatencyHistogram(value *float64) {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return
	}
	if b.latencyHistogram == nil {
		b.latencyHistogram = make(map[int32]uint32)
	}
	b.latencyHistogram[latencyHistogramKey(*value)]++
}

func latencyHistogramKey(value float64) int32 {
	if value <= 0 {
		return latencyHistogramZeroKey
	}
	raw := math.Ceil(math.Log(value) * latencyHistogramLogMultiplier)
	if raw <= float64(latencyHistogramZeroKey+1) {
		return latencyHistogramZeroKey + 1
	}
	if raw >= float64(int64(1<<31)-1) {
		return int32(1<<31 - 1)
	}
	return int32(raw)
}

func latencyHistogramValue(key int32) float64 {
	if key == latencyHistogramZeroKey {
		return 0
	}
	upperBound := math.Pow(latencyHistogramGamma, float64(key))
	return 2 * upperBound / (latencyHistogramGamma + 1)
}

func (h *chartHistory) latestBucket(startMS int64) *chartHistoryBucket {
	if h.count == 0 {
		return nil
	}
	index := (h.start + h.count - 1) % len(h.items)
	if h.items[index].startMS != startMS {
		return nil
	}
	return &h.items[index]
}

func (h *chartHistory) appendBucket(bucket chartHistoryBucket) *chartHistoryBucket {
	var index int
	if h.count < len(h.items) {
		index = (h.start + h.count) % len(h.items)
		h.count++
	} else {
		index = h.start
		h.start = (h.start + 1) % len(h.items)
	}
	h.items[index] = bucket
	return &h.items[index]
}

func (h *chartHistory) Buckets() []chartHistoryBucket {
	values := make([]chartHistoryBucket, h.count)
	for index := 0; index < h.count; index++ {
		values[index] = h.items[(h.start+index)%len(h.items)]
	}
	return values
}

func (h *chartHistory) LoadBuckets(values []chartHistoryBucket) {
	h.start = 0
	h.count = 0
	if len(values) > len(h.items) {
		values = values[len(values)-len(h.items):]
	}
	for _, bucket := range values {
		h.appendBucket(bucket)
	}
}

func (h *chartHistory) SamplesBefore(target Target, cutoff int64) []Sample {
	values := make([]Sample, 0, h.count*2)
	for index := 0; index < h.count; index++ {
		bucket := h.items[(h.start+index)%len(h.items)]
		if bucket.latestMeasurementTS > 0 && bucket.latestMeasurementTS < cutoff {
			google := bucket.metrics[chartMetricGoogle].average()
			stage := StageTCP
			if target.Kind == ProbeKindProxyGoogle {
				stage = StageTLS
				if google != nil {
					stage = StageHTTP
				}
			}
			values = append(values, Sample{
				TargetID:        target.ID,
				TS:              bucket.latestMeasurementTS,
				ProbeKind:       target.Kind,
				Latency:         bucket.metrics[chartMetricLatency].average(),
				LocalProxy:      bucket.metrics[chartMetricLocalProxy].average(),
				Tunnel:          bucket.metrics[chartMetricTunnel].average(),
				RemoteFirstByte: bucket.metrics[chartMetricRemoteFirstByte].average(),
				TLS:             bucket.metrics[chartMetricTLS].average(),
				Google:          google,
				Stage:           stage,
				Status:          StatusSuccess,
				BucketMS:        chartHistoryBucketMS,
			})
		}
		if bucket.latestFailureTS > 0 && bucket.latestFailureTS < cutoff {
			stage := decodeChartStage(bucket.failureStage)
			status := decodeChartStatus(bucket.failureStatus)
			var latency *float64
			if bucket.failureLatencySet {
				latency = floatPointer(bucket.failureLatency)
			}
			var tls *float64
			if bucket.failureTLSSet {
				tls = floatPointer(bucket.failureTLS)
			}
			values = append(values, Sample{
				TargetID:   target.ID,
				TS:         bucket.latestFailureTS,
				ProbeKind:  target.Kind,
				Latency:    latency,
				TLS:        tls,
				Stage:      stage,
				Status:     status,
				LossReason: decodeChartLossReason(bucket.failureLossReason),
				BucketMS:   chartHistoryBucketMS,
			})
		}
	}
	sort.Slice(values, func(left, right int) bool {
		return values[left].TS < values[right].TS
	})
	return values
}

// SummariesBefore returns only buckets that end no later than cutoff. The raw
// ring owns every later probe, so excluding a straddling minute prevents the
// frontend from counting the same attempt in both representations.
func (h *chartHistory) SummariesBefore(cutoff int64) []ChartBucket {
	values := make([]ChartBucket, 0, h.count)
	for index := 0; index < h.count; index++ {
		bucket := h.items[(h.start+index)%len(h.items)]
		if bucket.startMS+chartHistoryBucketMS > cutoff {
			continue
		}
		latency := bucket.metrics[chartMetricLatency]
		tls := bucket.metrics[chartMetricTLS]
		values = append(values, ChartBucket{
			StartMS:            bucket.startMS,
			DurationMS:         chartHistoryBucketMS,
			TotalCount:         bucket.totalCount,
			SuccessCount:       bucket.successCount,
			TimeoutCount:       bucket.timeoutCount,
			RefusedCount:       bucket.refusedCount,
			TunnelSuccessCount: bucket.tunnelSuccessCount,
			TunnelTimeoutCount: bucket.tunnelTimeoutCount,
			LatencyCount:       latency.count,
			LatencySum:         latency.sum,
			LatencySumSquares:  latency.sumSquares,
			TLSCount:           tls.count,
			TLSSum:             tls.sum,
			TLSSumSquares:      tls.sumSquares,
			LatencyHistogram:   sortedLatencyHistogram(bucket.latencyHistogram),
		})
	}
	return values
}

func sortedLatencyHistogram(histogram map[int32]uint32) []ChartHistogramBin {
	values := make([]ChartHistogramBin, 0, len(histogram))
	for key, count := range histogram {
		values = append(values, ChartHistogramBin{
			ValueMS: latencyHistogramValue(key),
			Count:   count,
		})
	}
	sort.Slice(values, func(left, right int) bool {
		return values[left].ValueMS < values[right].ValueMS
	})
	return values
}

func encodeChartStage(stage string) byte {
	switch stage {
	case StageTCP:
		return chartStageTCP
	case StageLocalProxy:
		return chartStageLocalProxy
	case StageSOCKS:
		return chartStageSOCKS
	case StageTLS:
		return chartStageTLS
	case StageHTTP:
		return chartStageHTTP
	default:
		return chartStageUnknown
	}
}

func decodeChartStage(stage byte) string {
	switch stage {
	case chartStageTCP:
		return StageTCP
	case chartStageLocalProxy:
		return StageLocalProxy
	case chartStageSOCKS:
		return StageSOCKS
	case chartStageTLS:
		return StageTLS
	case chartStageHTTP:
		return StageHTTP
	default:
		return ""
	}
}

func encodeChartStatus(status string) byte {
	switch status {
	case StatusTimeout:
		return chartStatusTimeout
	case StatusRefused:
		return chartStatusRefused
	case StatusDNSError:
		return chartStatusDNSError
	case StatusNoRoute:
		return chartStatusNoRoute
	case StatusLocalProxyRefused:
		return chartStatusLocalProxyRefused
	case StatusLocalProxyTimeout:
		return chartStatusLocalProxyTimeout
	case StatusLocalProxyError:
		return chartStatusLocalProxyError
	case StatusSOCKSAuthFailed:
		return chartStatusSOCKSAuthFailed
	case StatusSOCKSRejected:
		return chartStatusSOCKSRejected
	case StatusSOCKSProtocol:
		return chartStatusSOCKSProtocol
	case StatusTLSError:
		return chartStatusTLSError
	case StatusTLSCertificate:
		return chartStatusTLSCertificate
	case StatusHTTPError:
		return chartStatusHTTPError
	case StatusUnexpectedHTTP:
		return chartStatusUnexpectedHTTP
	case StatusPacketLoss:
		return chartStatusPacketLoss
	case StatusOther:
		return chartStatusOther
	default:
		return chartStatusUnknown
	}
}

func decodeChartStatus(status byte) string {
	switch status {
	case chartStatusTimeout:
		return StatusTimeout
	case chartStatusRefused:
		return StatusRefused
	case chartStatusDNSError:
		return StatusDNSError
	case chartStatusNoRoute:
		return StatusNoRoute
	case chartStatusLocalProxyRefused:
		return StatusLocalProxyRefused
	case chartStatusLocalProxyTimeout:
		return StatusLocalProxyTimeout
	case chartStatusLocalProxyError:
		return StatusLocalProxyError
	case chartStatusSOCKSAuthFailed:
		return StatusSOCKSAuthFailed
	case chartStatusSOCKSRejected:
		return StatusSOCKSRejected
	case chartStatusSOCKSProtocol:
		return StatusSOCKSProtocol
	case chartStatusTLSError:
		return StatusTLSError
	case chartStatusTLSCertificate:
		return StatusTLSCertificate
	case chartStatusHTTPError:
		return StatusHTTPError
	case chartStatusUnexpectedHTTP:
		return StatusUnexpectedHTTP
	case chartStatusPacketLoss:
		return StatusPacketLoss
	case chartStatusOther:
		return StatusOther
	default:
		return StatusOther
	}
}

func encodeChartLossReason(reason string) byte {
	switch reason {
	case LossReasonLatencySpike:
		return chartLossReasonLatencySpike
	default:
		return chartLossReasonUnknown
	}
}

func decodeChartLossReason(reason byte) string {
	switch reason {
	case chartLossReasonLatencySpike:
		return LossReasonLatencySpike
	default:
		return ""
	}
}
