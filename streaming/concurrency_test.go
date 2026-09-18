package streaming_test

// Real concurrency test (roadmap Phase 19: Performance Testing) — the
// dev-scale analog of doc 09 §8's "500 concurrent broadcasts" load
// target. This process has no ingest edge nodes or GPU transcoders to
// actually load-test against; what it can prove is that the session
// bookkeeping every one of those 500 broadcasts would go through
// (MemSessionRepo.Create, StartBroadcast's credential issuance) stays
// correct — unique session IDs, no lost or duplicated sessions — when
// hit concurrently.

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/lumena/streaming"
)

func TestConcurrentStartBroadcast_UniqueSessionsNoDataLoss(t *testing.T) {
	svc := newService()
	ctx := context.Background()

	const n = 500
	var wg sync.WaitGroup
	sessions := make([]*streaming.StreamSession, n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sess, _, err := svc.StartBroadcast(ctx, fmt.Sprintf("room-%d", i), fmt.Sprintf("host-%d", i), "US")
			sessions[i] = sess
			errs[i] = err
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: unexpected error starting broadcast: %v", i, errs[i])
		}
		if sessions[i] == nil || sessions[i].ID == "" {
			t.Fatalf("goroutine %d: got nil session or empty ID", i)
		}
		if seen[sessions[i].ID] {
			t.Fatalf("duplicate session ID %q assigned under concurrent StartBroadcast calls", sessions[i].ID)
		}
		seen[sessions[i].ID] = true
	}
	if len(seen) != n {
		t.Fatalf("expected %d distinct sessions, got %d", n, len(seen))
	}
}
