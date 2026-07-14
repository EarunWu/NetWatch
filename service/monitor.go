package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultSampleCapacity = 900

var errMonitorClosed = errors.New("monitor is shutting down")

type targetRuntime struct {
	target Target
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.RWMutex
	samples  *sampleRing
	chart    *chartHistory
	scratch  []float64
	baseline latencyBaseline
}

func newTargetRuntime(target Target, capacity int, history []Sample, chartBuckets []chartHistoryBucket) *targetRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &targetRuntime{
		target:  target,
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
		samples: newSampleRing(capacity),
		chart:   newChartHistory(chartHistoryCapacity),
		scratch: make([]float64, 0, capacity),
	}
	runtime.samples.Load(history)
	runtime.baseline.load(target, history)
	if len(chartBuckets) > 0 {
		runtime.chart.LoadBuckets(chartBuckets)
	} else {
		for _, sample := range history {
			runtime.chart.Add(sample)
		}
	}
	return runtime
}

func (r *targetRuntime) add(sample Sample, withStats bool) (Sample, Stats, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sample = r.baseline.classify(r.target, sample)
	r.samples.Add(sample)
	r.chart.Add(sample)
	if !withStats {
		return sample, Stats{}, false
	}
	stats, scratch := calculateRingStats(r.samples, r.scratch)
	r.scratch = scratch
	return sample, stats, true
}

func (r *targetRuntime) snapshot() TargetSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	samples := r.samples.Values()
	cutoff := int64(^uint64(0) >> 1)
	if len(samples) > 0 {
		cutoff = samples[0].TS
	}
	chartSamples := r.chart.SamplesBefore(r.target, cutoff)
	chartBuckets := r.chart.SummariesBefore(cutoff)
	stats, scratch := calculateRingStats(r.samples, r.scratch)
	r.scratch = scratch
	return TargetSnapshot{
		Target:       r.target,
		Stats:        stats,
		Samples:      samples,
		ChartSamples: chartSamples,
		ChartBuckets: chartBuckets,
	}
}

func (r *targetRuntime) history() ([]Sample, []chartHistoryBucket) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.samples.Values(), r.chart.Buckets()
}

type Monitor struct {
	mu       sync.RWMutex
	targets  map[string]*targetRuntime
	store    *ConfigStore
	hub      *eventHub
	capacity int
	closed   bool
}

func NewMonitor(targets []Target, store *ConfigStore, hub *eventHub, capacity int) (*Monitor, error) {
	if len(targets) > maxTargets {
		return nil, fmt.Errorf("at most %d targets are allowed", maxTargets)
	}
	if capacity < 1 {
		capacity = defaultSampleCapacity
	}
	monitor := &Monitor{
		targets:  make(map[string]*targetRuntime, len(targets)),
		store:    store,
		hub:      hub,
		capacity: capacity,
	}
	for _, raw := range targets {
		target, err := normalizeAndValidateTarget(raw)
		if err != nil {
			return nil, fmt.Errorf("target %q: %w", raw.Name, err)
		}
		if target.ID == "" {
			return nil, fmt.Errorf("target %q has no id", target.Name)
		}
		if _, duplicate := monitor.targets[target.ID]; duplicate {
			return nil, fmt.Errorf("duplicate target id %q", target.ID)
		}
		monitor.targets[target.ID] = newTargetRuntime(target, capacity, nil, nil)
	}
	for _, runtime := range monitor.targets {
		monitor.startRuntime(runtime)
	}
	return monitor, nil
}

func (m *Monitor) startRuntime(runtime *targetRuntime) {
	go func() {
		defer close(runtime.done)
		if !runtime.target.Enabled {
			return
		}

		// Keep starts aligned to a fixed schedule. A slow attempt never overlaps
		// with another; any ticks crossed by that attempt are skipped.
		interval := time.Duration(runtime.target.IntervalMS) * time.Millisecond
		next := time.Now().Add(initialDelay(runtime.target.ID))
		for {
			if !waitUntil(runtime.ctx, next) {
				return
			}
			scheduled := next
			sample := probeTarget(runtime.ctx, runtime.target)
			if runtime.ctx.Err() != nil {
				return
			}
			sample, stats, available := runtime.add(sample, m.hub.HasSubscribers())
			if available {
				m.hub.BroadcastJSON("sample", SampleEvent{Sample: sample, Stats: stats})
			}
			next = nextScheduledTime(scheduled, time.Now(), interval)
		}
	}()
}

func waitUntil(ctx context.Context, deadline time.Time) bool {
	delay := time.Until(deadline)
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextScheduledTime(previous, completed time.Time, interval time.Duration) time.Time {
	next := previous.Add(interval)
	if !next.Before(completed) {
		return next
	}
	delta := completed.Sub(next)
	missed := (delta-1)/interval + 1
	return next.Add(missed * interval)
}

func initialDelay(id string) time.Duration {
	var hash uint32 = 2166136261
	for i := 0; i < len(id); i++ {
		hash ^= uint32(id[i])
		hash *= 16777619
	}
	return time.Duration(hash%250) * time.Millisecond
}

func (m *Monitor) ListTargets() []Target {
	m.mu.RLock()
	defer m.mu.RUnlock()
	targets := m.targetsLocked()
	sortedTargets(targets)
	return targets
}

func (m *Monitor) TargetCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.targets)
}

func (m *Monitor) Snapshot() Snapshot {
	m.mu.RLock()
	targets := make([]TargetSnapshot, 0, len(m.targets))
	for _, runtime := range m.targets {
		targets = append(targets, runtime.snapshot())
	}
	m.mu.RUnlock()
	sort.Slice(targets, func(i, j int) bool {
		left := strings.ToLower(targets[i].Target.Name)
		right := strings.ToLower(targets[j].Target.Name)
		if left == right {
			return targets[i].Target.ID < targets[j].Target.ID
		}
		return left < right
	})
	return Snapshot{GeneratedAt: time.Now().UnixMilli(), Targets: targets}
}

func (m *Monitor) Add(raw Target) (Target, error) {
	raw.ID = ""
	target, err := normalizeAndValidateTarget(raw)
	if err != nil {
		return Target{}, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Target{}, errMonitorClosed
	}
	if len(m.targets) >= maxTargets {
		m.mu.Unlock()
		return Target{}, fmt.Errorf("at most %d targets are allowed", maxTargets)
	}
	target.ID, err = m.newIDLocked()
	if err != nil {
		m.mu.Unlock()
		return Target{}, err
	}
	desired := append(m.targetsLocked(), target)
	sortedTargets(desired)
	if m.store != nil {
		if err := m.store.Save(desired); err != nil {
			m.mu.Unlock()
			return Target{}, err
		}
	}
	runtime := newTargetRuntime(target, m.capacity, nil, nil)
	m.targets[target.ID] = runtime
	m.startRuntime(runtime)
	m.mu.Unlock()
	m.broadcastSnapshot()
	return target, nil
}

func (m *Monitor) Update(id string, raw Target) (Target, error) {
	if !validID(id) {
		return Target{}, errors.New("invalid target id")
	}
	if raw.ID != "" && raw.ID != id {
		return Target{}, errors.New("body id does not match URL")
	}
	raw.ID = id
	target, err := normalizeAndValidateTarget(raw)
	if err != nil {
		return Target{}, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Target{}, errMonitorClosed
	}
	old, exists := m.targets[id]
	if !exists {
		m.mu.Unlock()
		return Target{}, osErrNotExist{id: id}
	}
	desired := m.targetsLocked()
	for index := range desired {
		if desired[index].ID == id {
			desired[index] = target
			break
		}
	}
	sortedTargets(desired)
	if m.store != nil {
		if err := m.store.Save(desired); err != nil {
			m.mu.Unlock()
			return Target{}, err
		}
	}
	old.cancel()
	<-old.done
	var history []Sample
	var chartBuckets []chartHistoryBucket
	if sameProbeIdentity(old.target, target) {
		history, chartBuckets = old.history()
	}
	runtime := newTargetRuntime(target, m.capacity, history, chartBuckets)
	m.targets[id] = runtime
	m.startRuntime(runtime)
	m.mu.Unlock()
	m.broadcastSnapshot()
	return target, nil
}

func (m *Monitor) Delete(id string) error {
	if !validID(id) {
		return errors.New("invalid target id")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errMonitorClosed
	}
	runtime, exists := m.targets[id]
	if !exists {
		m.mu.Unlock()
		return osErrNotExist{id: id}
	}
	desired := make([]Target, 0, len(m.targets)-1)
	for targetID, candidate := range m.targets {
		if targetID != id {
			desired = append(desired, candidate.target)
		}
	}
	sortedTargets(desired)
	if m.store != nil {
		if err := m.store.Save(desired); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	runtime.cancel()
	<-runtime.done
	delete(m.targets, id)
	m.mu.Unlock()
	m.broadcastSnapshot()
	return nil
}

func (m *Monitor) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	runtimes := make([]*targetRuntime, 0, len(m.targets))
	for _, runtime := range m.targets {
		runtimes = append(runtimes, runtime)
		runtime.cancel()
	}
	m.mu.Unlock()
	for _, runtime := range runtimes {
		<-runtime.done
	}
}

func (m *Monitor) targetsLocked() []Target {
	targets := make([]Target, 0, len(m.targets))
	for _, runtime := range m.targets {
		targets = append(targets, runtime.target)
	}
	return targets
}

func (m *Monitor) newIDLocked() (string, error) {
	for attempts := 0; attempts < 4; attempts++ {
		bytes := make([]byte, 12)
		if _, err := rand.Read(bytes); err != nil {
			return "", fmt.Errorf("generate target id: %w", err)
		}
		id := hex.EncodeToString(bytes)
		if _, exists := m.targets[id]; !exists {
			return id, nil
		}
	}
	return "", errors.New("could not generate a unique target id")
}

func (m *Monitor) broadcastSnapshot() {
	if !m.hub.HasSubscribers() {
		return
	}
	m.hub.BroadcastJSON("snapshot", m.Snapshot())
}

type osErrNotExist struct{ id string }

func (e osErrNotExist) Error() string { return "target " + e.id + " was not found" }

func (e osErrNotExist) Is(target error) bool {
	_, ok := target.(osErrNotExist)
	return ok
}
