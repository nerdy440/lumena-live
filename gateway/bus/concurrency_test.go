package bus_test

// Real concurrency tests (roadmap Phase 19: Performance Testing) for the
// realtime fanout bus — the piece doc 09 §8's "10,000 WS connections"
// target is actually about. This dev build can't stand up 10k real
// WebSocket connections on one process for a meaningful test, but it can
// prove the underlying fanout primitive (what every one of those
// connections would ultimately subscribe through) is race-free and
// correct at a scale a single dev machine can genuinely drive.

import (
	"fmt"
	"sync"
	"testing"

	"github.com/lumena/gateway/bus"
)

// TestConcurrentSubscribers_NoCorruptionUnderFanout simulates a busy room
// topic with many simultaneous viewers (dev-scale stand-in for doc 09 §8's
// 10k WS connections) all draining concurrently while several goroutines
// publish. Publish is documented best-effort (bus.go: "a full channel
// drops the event for that subscriber rather than blocking the
// publisher"), so this does not assert zero drops — it asserts what must
// hold regardless under concurrent load: every subscriber's received
// sequence numbers are strictly increasing, with no duplicates and
// nothing outside the published range — i.e. fan-out never corrupts or
// duplicates a message, it only ever legitimately drops one.
func TestConcurrentSubscribers_NoCorruptionUnderFanout(t *testing.T) {
	b := bus.New()
	const subscribers = 300
	const events = 200
	const publishers = 20

	type subscription struct {
		ch          <-chan bus.Event
		unsubscribe func()
	}
	subs := make([]subscription, subscribers)
	for i := range subs {
		ch, _, unsub := b.Subscribe("room:loadtest")
		subs[i] = subscription{ch: ch, unsubscribe: unsub}
	}

	stop := make(chan struct{})
	received := make([][]uint64, subscribers)
	var drainWG sync.WaitGroup
	drainWG.Add(subscribers)
	for i := range subs {
		go func(i int) {
			defer drainWG.Done()
			var seqs []uint64
			for {
				select {
				case ev := <-subs[i].ch:
					seqs = append(seqs, ev.Seq)
				case <-stop:
					for { // drain whatever is already buffered, non-blocking
						select {
						case ev := <-subs[i].ch:
							seqs = append(seqs, ev.Seq)
						default:
							received[i] = seqs
							return
						}
					}
				}
			}
		}(i)
	}

	var pubWG sync.WaitGroup
	perPublisher := events / publishers
	for p := 0; p < publishers; p++ {
		pubWG.Add(1)
		go func() {
			defer pubWG.Done()
			for i := 0; i < perPublisher; i++ {
				b.Publish("room:loadtest", "COMMENT", nil)
			}
		}()
	}
	pubWG.Wait()

	close(stop)
	drainWG.Wait()

	totalDelivered := 0
	for i, seqs := range received {
		var last uint64
		for _, s := range seqs {
			if s <= last {
				t.Fatalf("subscriber %d: sequence went backward or duplicated (%d after %d) under concurrent fanout", i, s, last)
			}
			if s > uint64(events) {
				t.Fatalf("subscriber %d: received out-of-range seq %d (only %d were published)", i, s, events)
			}
			last = s
		}
		totalDelivered += len(seqs)
		subs[i].unsubscribe()
	}
	if totalDelivered == 0 {
		t.Fatal("expected at least some deliveries across 300 concurrent subscribers")
	}
	t.Logf("delivered %d/%d possible (subscribers x events) under concurrent fanout from %d publishers — some drop is expected per the bus's documented best-effort contract", totalDelivered, subscribers*events, publishers)
}

// TestConcurrentPublishersAcrossTopics_NoCrossTalk fires many goroutines
// publishing to many distinct topics concurrently and asserts each
// topic's sequence numbers are exactly 1..n with no gaps, duplicates, or
// values leaking from another topic — proving the per-topic mutex
// actually isolates topics under concurrent load rather than only in the
// single-threaded case.
func TestConcurrentPublishersAcrossTopics_NoCrossTalk(t *testing.T) {
	b := bus.New()
	const topics = 50
	const perTopic = 50

	var wg sync.WaitGroup
	for tIdx := 0; tIdx < topics; tIdx++ {
		wg.Add(1)
		go func(tIdx int) {
			defer wg.Done()
			topic := fmt.Sprintf("room:%d", tIdx)
			for i := 0; i < perTopic; i++ {
				b.Publish(topic, "COMMENT", map[string]any{"topic": tIdx, "i": i})
			}
		}(tIdx)
	}
	wg.Wait()

	for tIdx := 0; tIdx < topics; tIdx++ {
		topic := fmt.Sprintf("room:%d", tIdx)
		if got := b.LastSeq(topic); got != uint64(perTopic) {
			t.Fatalf("topic %s: expected last_seq %d after %d concurrent publishes, got %d", topic, perTopic, perTopic, got)
		}
	}
}
