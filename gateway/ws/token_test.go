package ws

import "testing"

func TestTokenStore_IssueAndConsumeOnce(t *testing.T) {
	s := NewTokenStore()
	tok := s.Issue("acc-1", "device-1")

	accountID, deviceID, ok := s.Consume(tok)
	if !ok || accountID != "acc-1" || deviceID != "device-1" {
		t.Fatalf("expected valid consume, got ok=%v account=%q device=%q", ok, accountID, deviceID)
	}

	_, _, ok = s.Consume(tok)
	if ok {
		t.Fatal("expected second consume of same token to fail (single-use)")
	}
}

func TestTokenStore_UnknownTokenFails(t *testing.T) {
	s := NewTokenStore()
	_, _, ok := s.Consume("does-not-exist")
	if ok {
		t.Fatal("expected unknown token to fail")
	}
}
