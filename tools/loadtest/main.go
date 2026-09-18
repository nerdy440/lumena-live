// Command loadtest drives the live running lumena-server with concurrent
// virtual users and reports real latency percentiles (roadmap Phase 19:
// Performance Testing).
//
// This is a genuine load-test against the real HTTP server over the real
// network stack (localhost TCP, not an in-process call) — not a
// simulation. What it cannot do is reach doc 09 §8's production targets
// (500 concurrent broadcasts, 50k viewers, 10k WS connections): those
// assume a CDN, GPU transcoders, multiple ingest edge nodes, and a
// distributed deployment, none of which exist for a single `go run` dev
// server on one machine. This tool reports what this single process can
// actually sustain, honestly, at a concurrency a dev laptop can drive
// without becoming the bottleneck itself.
//
// Usage:
//
//	go run ./tools/loadtest -url http://localhost:8080 -workers 200 -duration 10s
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	baseURL := flag.String("url", "http://localhost:8080", "base URL of the running lumena-server")
	workers := flag.Int("workers", 200, "concurrent virtual users")
	duration := flag.Duration("duration", 10*time.Second, "how long to sustain load")
	flag.Parse()

	client := &http.Client{Timeout: 5 * time.Second}
	stop := time.Now().Add(*duration)

	var mu sync.Mutex
	var latencies []time.Duration
	var successes, failures int64

	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(stop) {
				start := time.Now()
				resp, err := client.Get(*baseURL + "/api/v1/feed?tab=for_you&limit=12")
				elapsed := time.Since(start)
				if err != nil {
					atomic.AddInt64(&failures, 1)
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					atomic.AddInt64(&failures, 1)
					continue
				}
				atomic.AddInt64(&successes, 1)
				mu.Lock()
				latencies = append(latencies, elapsed)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(latencies) == 0 {
		fmt.Println("no successful requests — is the server running at", *baseURL, "?")
		return
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	pct := func(p float64) time.Duration {
		idx := int(float64(len(latencies)-1) * p)
		return latencies[idx]
	}

	total := successes + failures
	fmt.Printf("lumena-server load test: %d workers, %s duration, target %s\n", *workers, *duration, *baseURL)
	fmt.Printf("  requests:    %d total, %d ok, %d failed (%.2f%% error rate)\n", total, successes, failures, 100*float64(failures)/float64(total))
	fmt.Printf("  throughput:  %.1f req/s\n", float64(successes)/duration.Seconds())
	fmt.Printf("  latency:     p50=%s  p95=%s  p99=%s  max=%s\n", pct(0.50), pct(0.95), pct(0.99), latencies[len(latencies)-1])
}
