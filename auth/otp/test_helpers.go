// Package otp — test helpers (only compiled when running tests).
// These methods expose internals needed by integration tests.
// They must not be available in production builds.
// In a real monorepo: use build tags (//go:build !production).
package otp

import (
	"context"
	"errors"
)

// GetChallengeForTest returns the challenge for testing.
// NOT FOR PRODUCTION USE.
func (s *Service) GetChallengeForTest(ctx context.Context, challengeID string) (*Challenge, error) {
	return s.store.GetChallenge(ctx, challengeID)
}

// InjectCodeForTest retrieves the actual generated code from an in-memory store
// so that tests can verify it without SMS delivery.
// Works only with MemoryStore.
func (s *Service) InjectCodeForTest(ctx context.Context, challengeID string) string {
	mem, ok := s.store.(*MemoryStore)
	if !ok {
		panic("InjectCodeForTest requires MemoryStore")
	}
	ch, err := mem.GetChallenge(ctx, challengeID)
	if err != nil {
		return ""
	}

	// The code hash is XOR-folded ASCII — reverse by brute-force (it's 6 digits).
	// This is only viable because the test code range is small (000000–999999).
	for i := 0; i < 1_000_000; i++ {
		candidate := codePad(i)
		if hashesMatch(hashCode(candidate), ch.CodeHash) {
			return candidate
		}
	}
	return ""
}

func codePad(n int) string {
	s := ""
	for i := 5; i >= 0; i-- {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func hashesMatch(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Ensure errors are accessible
var _ = errors.New
