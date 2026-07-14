package main

import (
	"sync/atomic"
	"testing"
)

type marshalCounter struct {
	calls *atomic.Int32
}

func (c marshalCounter) MarshalJSON() ([]byte, error) {
	c.calls.Add(1)
	return []byte(`{"ok":true}`), nil
}

func TestHubSkipsMarshalWithoutSubscribers(t *testing.T) {
	hub := newEventHub()
	var calls atomic.Int32
	hub.BroadcastJSON("sample", marshalCounter{calls: &calls})
	if calls.Load() != 0 {
		t.Fatalf("marshaled %d times without subscribers", calls.Load())
	}
	channel, ok := hub.Subscribe()
	if !ok {
		t.Fatal("first subscriber was rejected")
	}
	defer hub.Unsubscribe(channel)
	hub.BroadcastJSON("sample", marshalCounter{calls: &calls})
	if calls.Load() != 1 {
		t.Fatalf("expected one marshal with subscriber, got %d", calls.Load())
	}
	<-channel
}

func TestHubLimitsSSEClients(t *testing.T) {
	hub := newEventHub()
	channels := make([]chan serverEvent, 0, maxSSEClients)
	for index := 0; index < maxSSEClients; index++ {
		channel, ok := hub.Subscribe()
		if !ok {
			t.Fatalf("subscriber %d was unexpectedly rejected", index+1)
		}
		channels = append(channels, channel)
	}
	if channel, ok := hub.Subscribe(); ok || channel != nil {
		t.Fatal("ninth SSE subscriber was accepted")
	}
	for _, channel := range channels {
		hub.Unsubscribe(channel)
	}
}

func TestHubCloseDisconnectsAndRejectsSubscribers(t *testing.T) {
	hub := newEventHub()
	channel, ok := hub.Subscribe()
	if !ok {
		t.Fatal("subscriber was rejected")
	}
	hub.Close()
	hub.Close()
	if _, open := <-channel; open {
		t.Fatal("subscriber channel remained open")
	}
	if hub.HasSubscribers() {
		t.Fatal("subscriber count was not reset")
	}
	if channel, ok := hub.Subscribe(); ok || channel != nil {
		t.Fatal("subscriber was accepted after close")
	}
}
