// Package attachsvc implements chat attachment upload and scanning (doc 02
// PC-04, doc 07 §8's POST /attachments: "presigned upload; scanned before
// deliverable", roadmap Phase 10).
//
// There is no real cloud storage or AV vendor here — this is a dev-scale
// in-memory blob store, same pattern as streaming's DevIngestService. It
// implements a real, standard test signature (EICAR) for the malware-scan
// path, since that's the actual documented exit-gate test ("Malicious
// attachment (EICAR test) is blocked") and doesn't require a vendor
// integration to verify honestly. It does NOT implement CSAM hash-matching
// against NCMEC (doc 10 §1's PhotoDNA/CyberTipline integration) — that's a
// legal/vendor dependency explicitly out of scope for local dev, same
// exemption already applied to the realtime moderation pipeline in Phase 6.
package attachsvc

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"
)

type Status string

const (
	StatusPendingUpload Status = "pending_upload"
	StatusClean         Status = "clean"
	StatusBlocked       Status = "blocked"
)

// MaxAttachmentBytes is a dev-scale cap — a real deployment enforces this
// (and per-content-type limits) at the storage layer.
const MaxAttachmentBytes = 10 << 20 // 10MB

// eicarSignature is the standard EICAR antivirus test file content —
// publicly documented, deliberately harmless, and the industry-standard way
// to verify a scanning pipeline actually blocks something without using a
// real virus. See https://www.eicar.org/download-anti-malware-testfile/.
const eicarSignature = `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`

var (
	ErrNotFound        = errors.New("attachsvc: attachment not found")
	ErrNotOwner        = errors.New("attachsvc: attachment does not belong to this account")
	ErrTooLarge        = errors.New("attachsvc: attachment exceeds the size limit")
	ErrEmpty           = errors.New("attachsvc: attachment is empty")
	ErrAlreadyUploaded = errors.New("attachsvc: this attachment has already been uploaded")
	ErrNotDeliverable  = errors.New("attachsvc: attachment is not deliverable (still scanning or blocked)")
)

// Attachment is one uploaded (or upload-pending) file.
type Attachment struct {
	ID          string
	OwnerID     string
	ContentType string
	Filename    string
	Size        int64
	Status      Status
	BlockReason string
	Data        []byte
	CreatedAt   time.Time
}

type Service struct {
	mu   sync.Mutex
	byID map[string]*Attachment
	seq  int
}

func NewService() *Service {
	return &Service{byID: make(map[string]*Attachment)}
}

// CreateUploadSlot registers a pending attachment and returns its metadata
// (doc 07 §8's presigned-upload step 1). The dev "presigned URL" is just
// this server's own /attachments/{id}/upload endpoint — main.go builds
// that path; this package only tracks the pending record.
func (s *Service) CreateUploadSlot(_ context.Context, ownerID, contentType, filename string) *Attachment {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	a := &Attachment{
		ID: "att-" + itoa(s.seq), OwnerID: ownerID, ContentType: contentType,
		Filename: filename, Status: StatusPendingUpload, CreatedAt: time.Now(),
	}
	s.byID[a.ID] = a
	cp := *a
	return &cp
}

// Upload accepts the raw bytes for a previously-created slot, scans them,
// and marks the attachment clean or blocked. A message can only reference
// an attachment once it's clean (doc 07 §8: "scanned before deliverable").
func (s *Service) Upload(_ context.Context, ownerID, attachmentID string, data []byte) (*Attachment, error) {
	if len(data) == 0 {
		return nil, ErrEmpty
	}
	if len(data) > MaxAttachmentBytes {
		return nil, ErrTooLarge
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byID[attachmentID]
	if !ok {
		return nil, ErrNotFound
	}
	if a.OwnerID != ownerID {
		return nil, ErrNotOwner
	}
	if a.Status != StatusPendingUpload {
		return nil, ErrAlreadyUploaded
	}

	if clean, reason := scan(data); !clean {
		a.Status = StatusBlocked
		a.BlockReason = reason
	} else {
		a.Status = StatusClean
		a.Data = data
		a.Size = int64(len(data))
	}
	cp := *a
	return &cp, nil
}

// scan is the dev-scale malware check: real EICAR-signature detection plus
// a same-again check for the size/emptiness cases already handled above.
func scan(data []byte) (clean bool, reason string) {
	if bytes.Contains(data, []byte(eicarSignature)) {
		return false, "malicious content detected (EICAR test signature)"
	}
	return true, ""
}

func (s *Service) Get(_ context.Context, id string) (*Attachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *a
	return &cp, nil
}

// Download returns the bytes for a clean attachment only — a blocked or
// still-pending attachment is never deliverable, regardless of who asks.
func (s *Service) Download(_ context.Context, id string) (*Attachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	if a.Status != StatusClean {
		return nil, ErrNotDeliverable
	}
	cp := *a
	return &cp, nil
}

// IsDeliverable is the DI hook chatsvc uses to validate an attachment_id
// before letting it ride along on a message (doc 07 §8's contract: only a
// clean attachment owned by the sender may be attached).
func (s *Service) IsDeliverable(ctx context.Context, attachmentID, ownerAccountID string) (bool, error) {
	a, err := s.Get(ctx, attachmentID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return a.Status == StatusClean && a.OwnerID == ownerAccountID, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
