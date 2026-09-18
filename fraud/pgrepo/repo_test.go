package pgrepo_test

import (
	"context"
	"os"
	"sort"
	"testing"

	"github.com/lumena/db"
	"github.com/lumena/fraud/pgrepo"
)

func newTestRepos(t *testing.T) (*pgrepo.DeviceRepo, *pgrepo.DisputeRepo) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping Postgres integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := db.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return pgrepo.NewDeviceRepo(pool), pgrepo.NewDisputeRepo(pool)
}

func TestDeviceRepo_RecordClustersAccountsOnSharedDevice(t *testing.T) {
	devices, _ := newTestRepos(t)
	ctx := context.Background()

	accounts1, err := devices.Record(ctx, "alice", "device-xyz")
	if err != nil || len(accounts1) != 1 || accounts1[0] != "alice" {
		t.Fatalf("first record: %+v err=%v", accounts1, err)
	}

	accounts2, err := devices.Record(ctx, "bob", "device-xyz")
	if err != nil {
		t.Fatalf("second record: %v", err)
	}
	sort.Strings(accounts2)
	if len(accounts2) != 2 || accounts2[0] != "alice" || accounts2[1] != "bob" {
		t.Fatalf("expected both alice and bob clustered on device-xyz, got %+v", accounts2)
	}

	// Re-recording the same (account, device) pair must not duplicate it.
	accounts3, err := devices.Record(ctx, "alice", "device-xyz")
	if err != nil || len(accounts3) != 2 {
		t.Fatalf("re-record should stay at 2 accounts, got %+v err=%v", accounts3, err)
	}

	lookup, err := devices.AccountsForDevice(ctx, "device-xyz")
	if err != nil || len(lookup) != 2 {
		t.Fatalf("AccountsForDevice: %+v err=%v", lookup, err)
	}

	none, err := devices.AccountsForDevice(ctx, "device-never-seen")
	if err != nil || len(none) != 0 {
		t.Fatalf("expected empty result for unseen device, got %+v err=%v", none, err)
	}
}

func TestDisputeRepo_IncrementAccumulatesPerAccount(t *testing.T) {
	_, disputes := newTestRepos(t)
	ctx := context.Background()

	n1, err := disputes.Increment(ctx, "alice")
	if err != nil || n1 != 1 {
		t.Fatalf("first increment = %d, want 1 (err=%v)", n1, err)
	}
	n2, err := disputes.Increment(ctx, "alice")
	if err != nil || n2 != 2 {
		t.Fatalf("second increment = %d, want 2 (err=%v)", n2, err)
	}
	// A different account starts fresh.
	nBob, err := disputes.Increment(ctx, "bob")
	if err != nil || nBob != 1 {
		t.Fatalf("bob's first increment = %d, want 1 (err=%v)", nBob, err)
	}
}
