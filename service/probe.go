package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	wsaeAddrNotAvailable syscall.Errno = 10049
	wsaeNetworkDown      syscall.Errno = 10050
	wsaeNetworkUnreach   syscall.Errno = 10051
	wsaeTimedOut         syscall.Errno = 10060
	wsaeConnRefused      syscall.Errno = 10061
	wsaeHostUnreach      syscall.Errno = 10065
)

func probeTCP(parent context.Context, target Target) Sample {
	return probeTCPWithResolver(parent, target, resolveHost)
}

type hostResolver func(context.Context, string, time.Duration) ([]string, error)

type tcpDialAttempt struct {
	address string
	dial    func(context.Context, string) (net.Conn, error)
}

func probeTCPWithResolver(parent context.Context, target Target, resolver hostResolver) Sample {
	host := strings.TrimPrefix(strings.TrimSuffix(target.Host, "]"), "[")
	addresses, err := resolver(parent, host, time.Duration(target.TimeoutMS)*time.Millisecond)
	if err != nil {
		return Sample{
			TargetID: target.ID,
			TS:       time.Now().UnixMilli(),
			Status:   StatusDNSError,
			Message:  truncateMessage(err.Error(), 240),
		}
	}

	attempts, preparationErr := prepareTCPDialAttempts(addresses, target)
	likelyFakeIP := target.BypassTUN && resolvedContainsLikelyFakeIP(host, addresses)
	if len(attempts) == 0 {
		if preparationErr == nil {
			preparationErr = newTUNBypassError("没有与目标地址族匹配的物理网卡")
		}
		sample := failedProbeSample(target.ID, time.Now(), StatusTUNBypassError, preparationErr)
		sample.Message = truncateMessage(appendFakeIPHint(sample.Message, likelyFakeIP), 240)
		return sample
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(target.TimeoutMS)*time.Millisecond)
	defer cancel()
	started := time.Now()
	var lastError error
	lastStatus := StatusOther
	for index, attempt := range attempts {
		connection, dialError := attempt.dial(ctx, attempt.address)
		completed := time.Now()
		if dialError == nil {
			latency := math.Round(float64(completed.Sub(started).Microseconds())/1000*1000) / 1000
			_ = connection.Close()
			return Sample{TargetID: target.ID, TS: completed.UnixMilli(), Latency: &latency, Status: StatusSuccess}
		}

		lastError = dialError
		if isTUNBypassError(dialError) {
			lastStatus = StatusTUNBypassError
		} else {
			lastStatus = classifyProbeError(dialError)
		}
		if target.BypassTUN && shouldInvalidateInterfaceCache(dialError) {
			bypassInterfaceCatalog.invalidate()
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			lastStatus = StatusTimeout
		}
		if lastStatus == StatusTimeout || index == len(attempts)-1 {
			sample := failedProbeSample(target.ID, completed, lastStatus, lastError)
			sample.Message = truncateMessage(appendFakeIPHint(sample.Message, likelyFakeIP), 240)
			return sample
		}
		if lastStatus != StatusNoRoute && lastStatus != StatusRefused && lastStatus != StatusOther && lastStatus != StatusTUNBypassError {
			sample := failedProbeSample(target.ID, completed, lastStatus, lastError)
			sample.Message = truncateMessage(appendFakeIPHint(sample.Message, likelyFakeIP), 240)
			return sample
		}
	}
	sample := failedProbeSample(target.ID, time.Now(), lastStatus, lastError)
	sample.Message = truncateMessage(appendFakeIPHint(sample.Message, likelyFakeIP), 240)
	return sample
}

func prepareTCPDialAttempts(addresses []string, target Target) ([]tcpDialAttempt, error) {
	attempts := make([]tcpDialAttempt, 0, len(addresses))
	if !target.BypassTUN {
		dialer := &net.Dialer{}
		for _, resolved := range addresses {
			attempts = append(attempts, tcpDialAttempt{
				address: net.JoinHostPort(resolved, strconv.Itoa(target.Port)),
				dial: func(ctx context.Context, address string) (net.Conn, error) {
					return dialer.DialContext(ctx, "tcp", address)
				},
			})
		}
		return attempts, nil
	}

	var lastError error
	for _, resolved := range addresses {
		plan, err := prepareBypassDialPlan(resolved, target.BypassInterfaceID)
		if err != nil {
			lastError = err
			continue
		}
		attempts = append(attempts, tcpDialAttempt{
			address: net.JoinHostPort(resolved, strconv.Itoa(target.Port)),
			dial:    plan.dial,
		})
	}
	return attempts, lastError
}

func failedProbeSample(targetID string, completed time.Time, status string, err error) Sample {
	sample := Sample{TargetID: targetID, TS: completed.UnixMilli(), Status: status}
	if err != nil {
		sample.Message = truncateMessage(err.Error(), 240)
	}
	return sample
}

type ipLookup func(context.Context, string) ([]net.IPAddr, error)

func resolveHost(parent context.Context, host string, timeout time.Duration) ([]string, error) {
	return resolveHostWithLookup(parent, host, timeout, net.DefaultResolver.LookupIPAddr)
}

func resolveHostWithLookup(parent context.Context, host string, timeout time.Duration, lookup ipLookup) ([]string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}, nil
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	resolved, err := lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	addresses := make([]string, 0, len(resolved))
	seen := make(map[string]struct{}, len(resolved))
	for _, address := range resolved {
		if address.IP == nil {
			continue
		}
		value := address.String()
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		addresses = append(addresses, value)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("DNS lookup for %s returned no addresses", host)
	}
	return addresses, nil
}

func classifyProbeError(err error) string {
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return StatusDNSError
	}
	if errors.Is(err, wsaeTimedOut) {
		return StatusTimeout
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, wsaeConnRefused) {
		return StatusRefused
	}
	if errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.EADDRNOTAVAIL) ||
		errors.Is(err, wsaeNetworkDown) || errors.Is(err, wsaeNetworkUnreach) || errors.Is(err, wsaeHostUnreach) || errors.Is(err, wsaeAddrNotAvailable) {
		return StatusNoRoute
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return StatusTimeout
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return StatusTimeout
	}
	return StatusOther
}

func truncateMessage(message string, max int) string {
	message = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, message)
	if len(message) <= max {
		return message
	}
	return message[:max]
}
