package fraud

import (
	"context"
	"sync"
	"time"
)

// MemDeviceRepo tracks device_hash -> set of accounts that have logged in
// from it, and account -> set of device_hash it has used (doc 06 §12's
// device_fingerprints, keyed (account_id, device_hash)).
type MemDeviceRepo struct {
	mu           sync.Mutex
	byDevice     map[string]map[string]*DeviceFingerprint // deviceHash -> accountID -> fingerprint
}

func NewMemDeviceRepo() *MemDeviceRepo {
	return &MemDeviceRepo{byDevice: make(map[string]map[string]*DeviceFingerprint)}
}

// Record upserts a fingerprint row and returns every account currently
// associated with deviceHash (including accountID itself).
func (r *MemDeviceRepo) Record(_ context.Context, accountID, deviceHash string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byDevice[deviceHash] == nil {
		r.byDevice[deviceHash] = make(map[string]*DeviceFingerprint)
	}
	now := time.Now()
	if fp, ok := r.byDevice[deviceHash][accountID]; ok {
		fp.LastSeen = now
	} else {
		r.byDevice[deviceHash][accountID] = &DeviceFingerprint{
			AccountID: accountID, DeviceHash: deviceHash, FirstSeen: now, LastSeen: now,
		}
	}
	accounts := make([]string, 0, len(r.byDevice[deviceHash]))
	for id := range r.byDevice[deviceHash] {
		accounts = append(accounts, id)
	}
	return accounts, nil
}

func (r *MemDeviceRepo) AccountsForDevice(_ context.Context, deviceHash string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fps := r.byDevice[deviceHash]
	out := make([]string, 0, len(fps))
	for id := range fps {
		out = append(out, id)
	}
	return out, nil
}

// MemDisputeRepo counts disputes per account — dev-scale stand-in for a
// real chargeback ledger (AF-05).
type MemDisputeRepo struct {
	mu     sync.Mutex
	counts map[string]int
}

func NewMemDisputeRepo() *MemDisputeRepo {
	return &MemDisputeRepo{counts: make(map[string]int)}
}

// Increment records one more dispute for accountID and returns the new total.
func (r *MemDisputeRepo) Increment(_ context.Context, accountID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[accountID]++
	return r.counts[accountID], nil
}
