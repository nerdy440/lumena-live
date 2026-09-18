package bus_test

import (
	"testing"

	"github.com/lumena/gateway/bus"
)

func TestPublishSubscribe_DeliversInOrderWithMonotonicSeq(t *testing.T) {
	b := bus.New()
	ch, lastSeq, unsub := b.Subscribe("room:1")
	defer unsub()
	if lastSeq != 0 {
		t.Fatalf("expected lastSeq 0 on empty topic, got %d", lastSeq)
	}

	b.Publish("room:1", "COMMENT", map[string]string{"body": "one"})
	b.Publish("room:1", "COMMENT", map[string]string{"body": "two"})

	ev1 := <-ch
	ev2 := <-ch
	if ev1.Seq != 1 || ev2.Seq != 2 {
		t.Fatalf("expected seq 1,2 got %d,%d", ev1.Seq, ev2.Seq)
	}
	if ev1.MsgID == "" || ev1.MsgID == ev2.MsgID {
		t.Fatalf("expected distinct non-empty msg_ids, got %q and %q", ev1.MsgID, ev2.MsgID)
	}
	if ev1.Topic != "room:1" {
		t.Fatalf("expected topic room:1, got %q", ev1.Topic)
	}
}

func TestBackfill_ReplaysGapWithinLimit(t *testing.T) {
	b := bus.New()
	for i := 0; i < 5; i++ {
		b.Publish("room:1", "LIKE", i)
	}

	events, ok := b.Backfill("room:1", 2)
	if !ok {
		t.Fatal("expected backfill to succeed within limit")
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events (seq 3,4,5), got %d", len(events))
	}
	for i, ev := range events {
		if ev.Seq != uint64(3+i) {
			t.Errorf("event %d: expected seq %d, got %d", i, 3+i, ev.Seq)
		}
	}
}

func TestBackfill_UpToDateReturnsEmpty(t *testing.T) {
	b := bus.New()
	b.Publish("room:1", "LIKE", 1)
	events, ok := b.Backfill("room:1", 1)
	if !ok || len(events) != 0 {
		t.Fatalf("expected ok with no events, got ok=%v len=%d", ok, len(events))
	}
}

func TestBackfill_GapTooLargeFails(t *testing.T) {
	b := bus.New()
	for i := 0; i < bus.BackfillLimit+10; i++ {
		b.Publish("room:1", "LIKE", i)
	}
	_, ok := b.Backfill("room:1", 1)
	if ok {
		t.Fatal("expected backfill to fail when gap exceeds BackfillLimit (CATCHUP_REQUIRED case)")
	}
}

func TestSubscribe_TopicsAreIndependent(t *testing.T) {
	b := bus.New()
	chA, _, unsubA := b.Subscribe("room:a")
	defer unsubA()
	chB, _, unsubB := b.Subscribe("room:b")
	defer unsubB()

	b.Publish("room:a", "COMMENT", "hi")

	select {
	case ev := <-chA:
		if ev.Topic != "room:a" {
			t.Fatalf("expected room:a event, got %q", ev.Topic)
		}
	default:
		t.Fatal("expected event on room:a subscriber")
	}
	select {
	case ev := <-chB:
		t.Fatalf("room:b subscriber should not receive room:a events, got %+v", ev)
	default:
	}
}

func TestUnsubscribe_StopsDelivery(t *testing.T) {
	b := bus.New()
	ch, _, unsub := b.Subscribe("room:1")
	unsub()
	b.Publish("room:1", "COMMENT", "hi")
	select {
	case ev := <-ch:
		t.Fatalf("expected no delivery after unsubscribe, got %+v", ev)
	default:
	}
}
