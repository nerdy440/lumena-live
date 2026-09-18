// Package bus is a Kafka-style in-process event bus: per-topic ordered
// delivery, monotonic per-topic sequence numbers, and a bounded backfill
// ring buffer, matching the fan-out contract in doc 08 §12 without an
// actual Kafka broker. A real deployment would replace this package's
// internals with a Kafka producer/consumer while keeping the same
// Publish/Subscribe/Backfill contract.
package bus

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// BackfillLimit is the maximum gap the bus will replay before telling the
// caller to fall back to a full REST refetch (doc 08 §11.3).
const BackfillLimit = 500

// Event is the server-to-client envelope from doc 08 §2.
type Event struct {
	MsgID   string `json:"msg_id"`
	Type    string `json:"type"`
	Topic   string `json:"topic"`
	Seq     uint64 `json:"seq"`
	Ts      string `json:"ts"`
	Payload any    `json:"payload"`
}

type topicState struct {
	mu   sync.RWMutex
	seq  uint64
	ring [BackfillLimit]Event // circular buffer, index = seq % BackfillLimit
	subs map[int]chan Event
	next int
}

// Bus fans events out to subscribers, one topicState per topic.
type Bus struct {
	mu     sync.Mutex
	topics map[string]*topicState
}

func New() *Bus {
	return &Bus{topics: make(map[string]*topicState)}
}

func (b *Bus) topic(name string) *topicState {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.topics[name]
	if !ok {
		t = &topicState{subs: make(map[int]chan Event)}
		b.topics[name] = t
	}
	return t
}

// Publish assigns the next sequence number for topic, stores the event for
// backfill, and fans it out to current subscribers. Delivery to a slow
// subscriber is best-effort: a full channel drops the event for that
// subscriber rather than blocking the publisher (the subscriber's next
// reconnect will backfill the gap).
//
// Assignment and delivery happen under the same lock acquisition
// (Phase 19 concurrency testing caught the earlier split version:
// unlocking between "assign seq" and "send to subscriber channels" let
// two concurrent Publish calls on the same topic be scheduled so a
// higher-seq event reached subscribers before a lower-seq one — silently
// breaking this package's own documented "ordered per-topic delivery"
// contract. Sends are still non-blocking (select+default), so lock hold
// time stays proportional to subscriber count, not to any subscriber's
// receive speed.
func (b *Bus) Publish(topic, typ string, payload any) Event {
	t := b.topic(topic)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	ev := Event{
		MsgID:   newMsgID(),
		Type:    typ,
		Topic:   topic,
		Seq:     t.seq,
		Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		Payload: payload,
	}
	t.ring[ev.Seq%BackfillLimit] = ev
	for _, ch := range t.subs {
		select {
		case ch <- ev:
		default:
		}
	}
	return ev
}

// Subscribe registers a subscriber on topic and returns a receive channel
// plus the topic's current last_seq (for the SUBSCRIBED ack) and an
// unsubscribe func.
func (b *Bus) Subscribe(topic string) (ch <-chan Event, lastSeq uint64, unsubscribe func()) {
	t := b.topic(topic)
	t.mu.Lock()
	id := t.next
	t.next++
	c := make(chan Event, 64)
	t.subs[id] = c
	lastSeq = t.seq
	t.mu.Unlock()

	return c, lastSeq, func() {
		t.mu.Lock()
		delete(t.subs, id)
		t.mu.Unlock()
	}
}

// Backfill returns events published after fromSeq. ok is false when the gap
// exceeds BackfillLimit (doc 08 §11.3: caller must fall back to REST).
func (b *Bus) Backfill(topic string, fromSeq uint64) (events []Event, ok bool) {
	t := b.topic(topic)
	t.mu.RLock()
	defer t.mu.RUnlock()

	if fromSeq >= t.seq {
		return nil, true
	}
	gap := t.seq - fromSeq
	if gap > BackfillLimit {
		return nil, false
	}
	events = make([]Event, 0, gap)
	for s := fromSeq + 1; s <= t.seq; s++ {
		ev := t.ring[s%BackfillLimit]
		if ev.Seq != s {
			// Overwritten by wraparound before we could read it under RLock —
			// treat as an unrecoverable gap.
			return nil, false
		}
		events = append(events, ev)
	}
	return events, true
}

// LastSeq returns the current sequence number for topic (0 if untouched).
func (b *Bus) LastSeq(topic string) uint64 {
	t := b.topic(topic)
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.seq
}

func newMsgID() string {
	var buf [10]byte
	_, _ = rand.Read(buf[:])
	return time.Now().UTC().Format("20060102150405.000000") + "-" + hex.EncodeToString(buf[:])
}
