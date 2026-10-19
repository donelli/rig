// Package bus is a tiny in-process pub/sub used to carry log lines and
// status transitions from the engine to whatever is watching (TUI, CLI,
// later a web UI) without coupling them together.
package bus

import "sync"

type EventType string

const (
	EventStatus EventType = "status"
	EventLog    EventType = "log"
)

type Event struct {
	Type    EventType `json:"type"`
	Service string    `json:"service"`
	Status  string    `json:"status,omitempty"` // set when Type == EventStatus
	Line    string    `json:"line,omitempty"`   // set when Type == EventLog
	Stream  string    `json:"stream,omitempty"` // "stdout" | "stderr", set when Type == EventLog
}

type Bus struct {
	mu   sync.RWMutex
	subs map[int]chan Event
	next int
}

func New() *Bus {
	return &Bus{subs: make(map[int]chan Event)}
}

// Subscribe returns a channel of events and an unsubscribe func. The
// channel is buffered; slow consumers drop events rather than blocking
// publishers.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	ch := make(chan Event, 256)
	b.subs[id] = ch
	unsubscribe := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if ch, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(ch)
		}
	}
	return ch, unsubscribe
}

func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
			// drop: a slow subscriber shouldn't stall the engine
		}
	}
}
