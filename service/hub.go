package main

import (
	"encoding/json"
	"sync"
	"sync/atomic"
)

const maxSSEClients = 8

type serverEvent struct {
	Type string
	Data []byte
}

type eventHub struct {
	mu              sync.Mutex
	clients         map[chan serverEvent]struct{}
	subscriberCount atomic.Int32
	closed          bool
}

func newEventHub() *eventHub {
	return &eventHub{clients: make(map[chan serverEvent]struct{})}
}

func (h *eventHub) Subscribe() (chan serverEvent, bool) {
	channel := make(chan serverEvent, 32)
	h.mu.Lock()
	if h.closed || len(h.clients) >= maxSSEClients {
		h.mu.Unlock()
		return nil, false
	}
	h.clients[channel] = struct{}{}
	h.subscriberCount.Add(1)
	h.mu.Unlock()
	return channel, true
}

func (h *eventHub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	for client := range h.clients {
		delete(h.clients, client)
		close(client)
	}
	h.subscriberCount.Store(0)
	h.mu.Unlock()
}

func (h *eventHub) Unsubscribe(channel chan serverEvent) {
	h.mu.Lock()
	if _, exists := h.clients[channel]; exists {
		delete(h.clients, channel)
		h.subscriberCount.Add(-1)
		close(channel)
	}
	h.mu.Unlock()
}

func (h *eventHub) HasSubscribers() bool {
	return h.subscriberCount.Load() > 0
}

func (h *eventHub) BroadcastJSON(eventType string, value any) {
	if !h.HasSubscribers() {
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	event := serverEvent{Type: eventType, Data: data}
	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		select {
		case client <- event:
		default:
			// Disconnect a stalled dashboard. EventSource reconnects and receives
			// a fresh initial snapshot, while probes remain independent from UI
			// backpressure.
			delete(h.clients, client)
			h.subscriberCount.Add(-1)
			close(client)
		}
	}
}
