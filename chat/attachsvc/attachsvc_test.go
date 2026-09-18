package attachsvc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lumena/chat/attachsvc"
)

func TestUpload_CleanFileBecomesDeliverable(t *testing.T) {
	s := attachsvc.NewService()
	ctx := context.Background()
	slot := s.CreateUploadSlot(ctx, "alice", "image/png", "photo.png")

	a, err := s.Upload(ctx, "alice", slot.ID, []byte("just a normal harmless file"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != attachsvc.StatusClean {
		t.Fatalf("expected clean, got %s", a.Status)
	}

	deliverable, err := s.IsDeliverable(ctx, slot.ID, "alice")
	if err != nil || !deliverable {
		t.Fatalf("expected deliverable=true, got %v err=%v", deliverable, err)
	}

	dl, err := s.Download(ctx, slot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(dl.Data) != "just a normal harmless file" {
		t.Fatalf("unexpected downloaded content: %q", dl.Data)
	}
}

func TestUpload_EICARTestFileIsBlocked(t *testing.T) {
	s := attachsvc.NewService()
	ctx := context.Background()
	slot := s.CreateUploadSlot(ctx, "alice", "application/octet-stream", "totally-safe.exe")

	eicar := []byte(`X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`)
	a, err := s.Upload(ctx, "alice", slot.ID, eicar)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != attachsvc.StatusBlocked {
		t.Fatalf("expected blocked for EICAR test file, got %s", a.Status)
	}
	if a.BlockReason == "" {
		t.Fatal("expected a non-empty block reason")
	}

	deliverable, _ := s.IsDeliverable(ctx, slot.ID, "alice")
	if deliverable {
		t.Fatal("expected a blocked attachment to never be deliverable")
	}
	_, err = s.Download(ctx, slot.ID)
	if !errors.Is(err, attachsvc.ErrNotDeliverable) {
		t.Fatalf("expected ErrNotDeliverable, got %v", err)
	}
}

func TestDownload_NotDeliverableUntilScanCompletes(t *testing.T) {
	s := attachsvc.NewService()
	ctx := context.Background()
	slot := s.CreateUploadSlot(ctx, "alice", "image/png", "photo.png")

	// No Upload() called yet — still pending_upload, scan hasn't run.
	_, err := s.Download(ctx, slot.ID)
	if !errors.Is(err, attachsvc.ErrNotDeliverable) {
		t.Fatalf("expected ErrNotDeliverable before scan completes, got %v", err)
	}
	deliverable, _ := s.IsDeliverable(ctx, slot.ID, "alice")
	if deliverable {
		t.Fatal("expected not deliverable before upload/scan")
	}
}

func TestUpload_WrongOwnerRejected(t *testing.T) {
	s := attachsvc.NewService()
	ctx := context.Background()
	slot := s.CreateUploadSlot(ctx, "alice", "image/png", "photo.png")

	_, err := s.Upload(ctx, "mallory", slot.ID, []byte("data"))
	if !errors.Is(err, attachsvc.ErrNotOwner) {
		t.Fatalf("expected ErrNotOwner, got %v", err)
	}
}

func TestUpload_TooLargeRejected(t *testing.T) {
	s := attachsvc.NewService()
	ctx := context.Background()
	slot := s.CreateUploadSlot(ctx, "alice", "image/png", "photo.png")

	big := make([]byte, attachsvc.MaxAttachmentBytes+1)
	_, err := s.Upload(ctx, "alice", slot.ID, big)
	if !errors.Is(err, attachsvc.ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}

func TestIsDeliverable_WrongOwnerCannotAttachSomeoneElsesFile(t *testing.T) {
	s := attachsvc.NewService()
	ctx := context.Background()
	slot := s.CreateUploadSlot(ctx, "alice", "image/png", "photo.png")
	s.Upload(ctx, "alice", slot.ID, []byte("clean data"))

	deliverable, err := s.IsDeliverable(ctx, slot.ID, "mallory")
	if err != nil {
		t.Fatal(err)
	}
	if deliverable {
		t.Fatal("expected mallory not to be able to attach alice's file to her own message")
	}
}
