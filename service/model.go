package main

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	minIntervalMS     = 500
	minNodeIntervalMS = 2000
	maxIntervalMS     = 60 * 60 * 1000
	minTimeoutMS      = 100
	maxTimeoutMS      = 60 * 1000
	maxTargets        = 100
)

const (
	ProbeKindDirectTCP   = "direct_tcp"
	ProbeKindProxyGoogle = "proxy_google"

	GoogleProbeHost = "www.google.com"
	GoogleProbePort = 443
	GoogleProbePath = "/generate_204"
)

const (
	StatusSuccess           = "success"
	StatusTimeout           = "timeout"
	StatusRefused           = "refused"
	StatusDNSError          = "dns_error"
	StatusNoRoute           = "no_route"
	StatusLocalProxyRefused = "local_proxy_refused"
	StatusLocalProxyTimeout = "local_proxy_timeout"
	StatusLocalProxyError   = "local_proxy_error"
	StatusSOCKSAuthFailed   = "socks_auth_failed"
	StatusSOCKSRejected     = "socks_rejected"
	StatusSOCKSProtocol     = "socks_protocol_error"
	StatusTLSError          = "tls_error"
	StatusTLSCertificate    = "tls_certificate_error"
	StatusHTTPError         = "http_error"
	StatusUnexpectedHTTP    = "unexpected_http_status"
	StatusPacketLoss        = "packet_loss"
	StatusTUNBypassError    = "tun_bypass_error"
	StatusOther             = "other"
)

const (
	LossReasonLatencySpike = "latency_spike"
)

const (
	StageTCP        = "tcp"
	StageLocalProxy = "local_proxy"
	StageSOCKS      = "socks"
	StageTLS        = "tls"
	StageHTTP       = "http"
)

// Target is either a direct TCP endpoint or a configurable TLS endpoint
// reached through a local SOCKS5 entry owned by the user's proxy client.
type Target struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Kind              string `json:"kind"`
	Host              string `json:"host"`
	Port              int    `json:"port"`
	ProxyHost         string `json:"proxy_host,omitempty"`
	ProxyPort         int    `json:"proxy_port,omitempty"`
	Google204Enabled  bool   `json:"google_204_enabled"`
	BypassTUN         bool   `json:"bypass_tun"`
	BypassInterfaceID string `json:"bypass_interface_id"`
	IntervalMS        int    `json:"interval_ms"`
	TimeoutMS         int    `json:"timeout_ms"`
	Enabled           bool   `json:"enabled"`
}

// Sample is the result of one scheduled logical probe. Latency remains present
// when a completed probe is classified as a latency spike, so charts and
// latency statistics retain the degraded experience even though the sample is
// counted as an estimated loss.
type Sample struct {
	TargetID        string   `json:"target_id"`
	TS              int64    `json:"ts"`
	ProbeKind       string   `json:"probe_kind,omitempty"`
	Latency         *float64 `json:"latency_ms,omitempty"`
	LocalProxy      *float64 `json:"local_proxy_ms,omitempty"`
	Tunnel          *float64 `json:"tunnel_ms,omitempty"`
	RemoteFirstByte *float64 `json:"remote_first_byte_ms,omitempty"`
	TLS             *float64 `json:"tls_ms,omitempty"`
	Google          *float64 `json:"google_ms,omitempty"`
	Stage           string   `json:"stage,omitempty"`
	HTTPStatus      int      `json:"http_status,omitempty"`
	Status          string   `json:"status"`
	LossReason      string   `json:"loss_reason,omitempty"`
	Message         string   `json:"message,omitempty"`
	BucketMS        int64    `json:"bucket_ms,omitempty"`
}

// Stats summarizes the samples currently retained in memory. Rates are
// percentages in the inclusive range 0..100.
type Stats struct {
	CurrentMS                *float64 `json:"current_ms"`
	P95MS                    *float64 `json:"p95_ms"`
	LocalProxyCurrentMS      *float64 `json:"local_proxy_current_ms"`
	TunnelCurrentMS          *float64 `json:"tunnel_current_ms"`
	RemoteFirstByteCurrentMS *float64 `json:"remote_first_byte_current_ms"`
	TLSCurrentMS             *float64 `json:"tls_current_ms"`
	GoogleCurrentMS          *float64 `json:"google_current_ms"`
	SuccessRate              float64  `json:"success_rate"`
	TimeoutRate              float64  `json:"timeout_rate"`
	RefusedRate              float64  `json:"refused_rate"`
	EstimatedLossRate        *float64 `json:"estimated_loss_rate"`
	TunnelTimeoutRate        *float64 `json:"tunnel_timeout_rate"`
	GoogleTimeoutRate        *float64 `json:"google_timeout_rate"`
	SampleCount              int      `json:"sample_count"`
	LastStatus               string   `json:"last_status,omitempty"`
	LastSampleAt             int64    `json:"last_sample_at,omitempty"`
	ConsecutiveFailure       int      `json:"consecutive_failures"`
}

// ChartHistogramBin is one sparse logarithmic latency bucket. ValueMS is the
// representative latency for Count completed measurements with a valid value.
type ChartHistogramBin struct {
	ValueMS float64 `json:"value_ms"`
	Count   uint32  `json:"count"`
}

// ChartBucket contains mergeable statistics for one completed chart-history
// minute. It intentionally carries aggregates rather than individual probes so
// longer dashboard ranges stay inexpensive in memory and over the API.
type ChartBucket struct {
	StartMS            int64               `json:"start_ms"`
	DurationMS         int64               `json:"duration_ms"`
	TotalCount         uint32              `json:"total_count"`
	SuccessCount       uint32              `json:"success_count"`
	TimeoutCount       uint32              `json:"timeout_count"`
	RefusedCount       uint32              `json:"refused_count"`
	TunnelSuccessCount uint32              `json:"tunnel_success_count"`
	TunnelTimeoutCount uint32              `json:"tunnel_timeout_count"`
	LatencyCount       uint32              `json:"latency_count"`
	LatencySum         float64             `json:"latency_sum"`
	LatencySumSquares  float64             `json:"latency_sum_squares"`
	TLSCount           uint32              `json:"tls_count"`
	TLSSum             float64             `json:"tls_sum"`
	TLSSumSquares      float64             `json:"tls_sum_squares"`
	LatencyHistogram   []ChartHistogramBin `json:"latency_histogram"`
}

type TargetSnapshot struct {
	Target       Target        `json:"target"`
	Stats        Stats         `json:"stats"`
	Samples      []Sample      `json:"samples"`
	ChartSamples []Sample      `json:"chart_samples,omitempty"`
	ChartBuckets []ChartBucket `json:"chart_buckets,omitempty"`
}

type Snapshot struct {
	GeneratedAt int64            `json:"generated_at"`
	Targets     []TargetSnapshot `json:"targets"`
}

type SampleEvent struct {
	Sample Sample `json:"sample"`
	Stats  Stats  `json:"stats"`
}

func normalizeAndValidateTarget(t Target) (Target, error) {
	t.Name = strings.TrimSpace(t.Name)
	t.Kind = strings.TrimSpace(t.Kind)
	t.Host = strings.TrimSpace(t.Host)
	t.ProxyHost = strings.TrimSpace(t.ProxyHost)
	t.BypassInterfaceID = strings.TrimSpace(t.BypassInterfaceID)
	if t.Kind == "" {
		t.Kind = ProbeKindDirectTCP
	}

	if t.ID != "" && !validID(t.ID) {
		return Target{}, errors.New("id must contain only letters, digits, '-' or '_' and be at most 64 characters")
	}
	if t.Name == "" || utf8.RuneCountInString(t.Name) > 80 || containsControl(t.Name) {
		return Target{}, errors.New("name must be 1 to 80 characters without control characters")
	}
	switch t.Kind {
	case ProbeKindDirectTCP:
		if err := validateHost(t.Host); err != nil {
			return Target{}, err
		}
		if t.Port < 1 || t.Port > 65535 {
			return Target{}, errors.New("port must be between 1 and 65535")
		}
		t.ProxyHost = ""
		t.ProxyPort = 0
		t.Google204Enabled = false
		if len(t.BypassInterfaceID) > 256 || containsControl(t.BypassInterfaceID) {
			return Target{}, errors.New("bypass_interface_id must be at most 256 characters without control characters")
		}
	case ProbeKindProxyGoogle:
		if t.Host == "" {
			t.Host = GoogleProbeHost
		}
		if t.Port == 0 {
			t.Port = GoogleProbePort
		}
		if err := validateHost(t.Host); err != nil {
			return Target{}, err
		}
		if t.Port < 1 || t.Port > 65535 {
			return Target{}, errors.New("port must be between 1 and 65535")
		}
		if !validLoopbackProxyHost(t.ProxyHost) {
			return Target{}, errors.New("proxy_host must be a loopback address or localhost")
		}
		if t.ProxyPort < 1 || t.ProxyPort > 65535 {
			return Target{}, errors.New("proxy_port must be between 1 and 65535")
		}
		if t.IntervalMS < minNodeIntervalMS {
			return Target{}, fmt.Errorf("node probe interval_ms must be at least %d", minNodeIntervalMS)
		}
		t.BypassTUN = false
		t.BypassInterfaceID = ""
	default:
		return Target{}, errors.New("kind must be direct_tcp or proxy_google")
	}
	if t.IntervalMS < minIntervalMS || t.IntervalMS > maxIntervalMS {
		return Target{}, fmt.Errorf("interval_ms must be between %d and %d", minIntervalMS, maxIntervalMS)
	}
	if t.TimeoutMS < minTimeoutMS || t.TimeoutMS > maxTimeoutMS {
		return Target{}, fmt.Errorf("timeout_ms must be between %d and %d", minTimeoutMS, maxTimeoutMS)
	}
	return t, nil
}

func validLoopbackProxyHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.TrimPrefix(strings.TrimSuffix(host, "]"), "["))
	return ip != nil && ip.IsLoopback()
}

func sameProbeIdentity(left, right Target) bool {
	return left.Kind == right.Kind &&
		strings.EqualFold(left.Host, right.Host) &&
		left.Port == right.Port &&
		strings.EqualFold(left.ProxyHost, right.ProxyHost) &&
		left.ProxyPort == right.ProxyPort &&
		left.Google204Enabled == right.Google204Enabled &&
		left.BypassTUN == right.BypassTUN &&
		left.BypassInterfaceID == right.BypassInterfaceID
}

func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func validateHost(host string) error {
	if host == "" || len(host) > 253 || containsControl(host) {
		return errors.New("host must be 1 to 253 characters without control characters")
	}
	if strings.ContainsAny(host, "/\\?#@") || strings.Contains(host, "://") {
		return errors.New("host must be an IP address or DNS name, without a scheme, path or port")
	}

	trimmedIP := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if net.ParseIP(trimmedIP) != nil {
		return nil
	}
	if strings.Contains(host, ":") {
		return errors.New("invalid IP address")
	}

	name := strings.TrimSuffix(host, ".")
	if name == "" {
		return errors.New("invalid DNS name")
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("invalid DNS name")
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
				continue
			}
			return errors.New("DNS names must use ASCII letters, digits, '-' or '_'")
		}
	}
	return nil
}

func sortedTargets(targets []Target) {
	sort.Slice(targets, func(i, j int) bool {
		left := strings.ToLower(targets[i].Name)
		right := strings.ToLower(targets[j].Name)
		if left == right {
			return targets[i].ID < targets[j].ID
		}
		return left < right
	})
}
