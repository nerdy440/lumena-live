// Package reconciler implements the order-recovery worker (doc 06 §10,
// roadmap Phase 9): every interval, it scans for orders stuck in a
// non-terminal state and re-attempts crediting. This is the structural fix
// for "I paid and got nothing" — a crash between store confirmation and
// ledger credit self-heals instead of becoming a support ticket.
package reconciler

import (
	"context"
	"log/slog"
	"time"

	"github.com/lumena/ledger"
)

// OrderAdvancer is the subset of ordersvc.Service the reconciler needs.
// Declared locally so this package doesn't import ordersvc directly —
// same decoupling pattern used throughout this workspace.
type OrderAdvancer interface {
	ListNonTerminal(ctx context.Context) ([]ledger.Order, error)
	Reconcile(ctx context.Context, order *ledger.Order) (*ledger.Order, error)
}

// Worker periodically retries every non-terminal order.
type Worker struct {
	svc      OrderAdvancer
	interval time.Duration
	log      *slog.Logger

	stop chan struct{}
	done chan struct{}
}

func New(svc OrderAdvancer, interval time.Duration, log *slog.Logger) *Worker {
	if log == nil {
		log = slog.Default()
	}
	return &Worker{svc: svc, interval: interval, log: log, stop: make(chan struct{}), done: make(chan struct{})}
}

// Start runs the reconciliation loop in a background goroutine.
func (w *Worker) Start(ctx context.Context) {
	go w.run(ctx)
}

// Stop signals the loop to exit and waits for it to actually stop.
func (w *Worker) Stop() {
	close(w.stop)
	<-w.done
}

func (w *Worker) run(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce scans and retries every non-terminal order once. Exported so
// tests can drive reconciliation deterministically instead of waiting on
// the ticker.
func (w *Worker) RunOnce(ctx context.Context) {
	orders, err := w.svc.ListNonTerminal(ctx)
	if err != nil {
		w.log.Error("reconciler: list non-terminal orders", "err", err)
		return
	}
	for i := range orders {
		order := orders[i]
		if _, err := w.svc.Reconcile(ctx, &order); err != nil {
			w.log.Warn("reconciler: retry did not complete order", "order_id", order.ID, "status", order.Status, "err", err)
		}
	}
}
