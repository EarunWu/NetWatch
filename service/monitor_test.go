package main

import (
	"testing"
	"time"
)

func TestUpdateAndDeleteStopOldRuntime(t *testing.T) {
	hub := newEventHub()
	target := Target{ID: "active", Name: "Active", Host: "192.0.2.1", Port: 81, IntervalMS: 60_000, TimeoutMS: 60_000, Enabled: true}
	monitor, err := NewMonitor([]Target{target}, nil, hub, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close()

	monitor.mu.RLock()
	firstRuntime := monitor.targets[target.ID]
	monitor.mu.RUnlock()
	target.Name = "Updated"
	started := time.Now()
	if _, err := monitor.Update(target.ID, target); err != nil {
		t.Fatalf("update: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("update did not promptly cancel the old runtime")
	}
	select {
	case <-firstRuntime.done:
	default:
		t.Fatal("old runtime is still running after update")
	}

	monitor.mu.RLock()
	secondRuntime := monitor.targets[target.ID]
	monitor.mu.RUnlock()
	started = time.Now()
	if err := monitor.Delete(target.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("delete did not promptly cancel the runtime")
	}
	select {
	case <-secondRuntime.done:
	default:
		t.Fatal("runtime is still running after delete")
	}
	if monitor.TargetCount() != 0 {
		t.Fatalf("deleted target still present: %d", monitor.TargetCount())
	}
}

func TestNextScheduledTimeSkipsMissedTicks(t *testing.T) {
	start := time.Unix(0, 0)
	interval := time.Second
	if got := nextScheduledTime(start, start.Add(500*time.Millisecond), interval); !got.Equal(start.Add(time.Second)) {
		t.Fatalf("early completion moved schedule to %s", got)
	}
	if got := nextScheduledTime(start, start.Add(2500*time.Millisecond), interval); !got.Equal(start.Add(3 * time.Second)) {
		t.Fatalf("missed ticks were not skipped: %s", got)
	}
	if got := nextScheduledTime(start, start.Add(2*time.Second), interval); !got.Equal(start.Add(2 * time.Second)) {
		t.Fatalf("an exactly due tick should run immediately: %s", got)
	}
}
