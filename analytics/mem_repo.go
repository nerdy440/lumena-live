package analytics

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemEventRepo is the dev stand-in for the Kafka-fed warehouse (doc 05's
// event pipeline) — append-only, in-process. See the package doc comment.
type MemEventRepo struct {
	mu     sync.Mutex
	seq    int
	events []Event
}

func NewMemEventRepo() *MemEventRepo {
	return &MemEventRepo{}
}

func (r *MemEventRepo) Append(_ context.Context, accountID string, eventType EventType) (*Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	e := Event{ID: fmt.Sprintf("evt-%06d", r.seq), Type: eventType, AccountID: accountID, CreatedAt: time.Now()}
	r.events = append(r.events, e)
	return &e, nil
}

func (r *MemEventRepo) ListSince(_ context.Context, since time.Time) ([]Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, e := range r.events {
		if !e.CreatedAt.Before(since) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (r *MemEventRepo) ListByType(_ context.Context, eventType EventType) ([]Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, e := range r.events {
		if e.Type == eventType {
			out = append(out, e)
		}
	}
	return out, nil
}
