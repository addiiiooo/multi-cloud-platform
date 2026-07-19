// Package store provides pulse-api's in-memory data store.
//
// The architecture deliberately keeps this service stateless with respect
// to any external datastore (no RDS/Cloud SQL — see the architecture
// document's Risks and Tradeoffs section). This package exists only to
// give the service one real, exercisable resource for its JSON route,
// backed by a bounded, concurrency-safe, fixed-capacity circular buffer
// that resets on restart. It is intentionally not a substitute for a real
// datastore and should never be treated as one past this demonstration
// purpose.
package store

import (
	"errors"
	"sync"
	"time"
)

// ErrEmptyMessage is returned when a caller tries to add a pulse with no content.
var ErrEmptyMessage = errors.New("message must not be empty")

// ErrMessageTooLong is returned when a message exceeds MaxMessageLength.
var ErrMessageTooLong = errors.New("message exceeds maximum length")

// MaxMessageLength bounds a single pulse's message size, preventing a
// caller from growing the store's memory footprint with oversized payloads.
const MaxMessageLength = 500

// Pulse is one recorded entry.
type Pulse struct {
	ID        int64     `json:"id"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// Store is a fixed-capacity, thread-safe, in-memory ring buffer of Pulses.
// Unlike a slice that is periodically re-sliced down to size, the backing
// array here is allocated once at the requested capacity and never grows,
// so memory use is genuinely bounded for the lifetime of the process, not
// just usually small.
type Store struct {
	mu       sync.Mutex
	capacity int
	nextID   int64
	items    []Pulse // fixed-size backing array, length == capacity
	start    int     // index of the oldest valid item
	count    int     // number of valid items currently stored (0..capacity)
}

// New creates a Store that retains at most capacity items, overwriting the
// oldest entry once full. A non-positive capacity is treated as 1 so the
// store is always usable.
func New(capacity int) *Store {
	if capacity <= 0 {
		capacity = 1
	}
	return &Store{
		capacity: capacity,
		items:    make([]Pulse, capacity),
	}
}

// Add validates and records a new pulse, overwriting the oldest entry if
// the store is at capacity.
func (s *Store) Add(message string) (Pulse, error) {
	if message == "" {
		return Pulse{}, ErrEmptyMessage
	}
	if len(message) > MaxMessageLength {
		return Pulse{}, ErrMessageTooLong
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	p := Pulse{
		ID:        s.nextID,
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}

	if s.count < s.capacity {
		writeIdx := (s.start + s.count) % s.capacity
		s.items[writeIdx] = p
		s.count++
	} else {
		s.items[s.start] = p
		s.start = (s.start + 1) % s.capacity
	}
	return p, nil
}

// List returns the current pulses, most recently created first. The
// returned slice is a fresh copy: callers can never observe or corrupt the
// store's internal state.
func (s *Store) List() []Pulse {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Pulse, s.count)
	for i := 0; i < s.count; i++ {
		idx := (s.start + s.count - 1 - i + s.capacity) % s.capacity
		out[i] = s.items[idx]
	}
	return out
}
