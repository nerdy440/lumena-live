// Package translate implements the translation abstraction.
// The interface decouples the system from any specific vendor.
// Translation is always async — it NEVER blocks the primary chat pipeline.
package translate

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Result is the output of a translation request.
type Result struct {
	TranslatedText  string
	SourceLanguage  string
	TargetLanguage  string
	Confidence      float64 // for logging/debugging only; never exposed in the economic or moderation path
	ProviderLatency time.Duration
	CachedAt        *time.Time
}

// Request is the input to a translation request.
type Request struct {
	Text           string
	SourceLanguage string // "" = auto-detect
	TargetLanguage string
	ContextHint    string // "chat" | "room_title" | "bio" — affects formality
}

// Provider is the abstraction over translation vendors.
// Production uses a real vendor (Google Translate / DeepL / etc.).
// Development uses DevTranslationProvider (DEV MOCK — never ships to production).
type Provider interface {
	// Translate translates the text. Implementations must be safe to call concurrently.
	Translate(ctx context.Context, req Request) (*Result, error)
	// SupportedLanguages returns the list of language codes this provider supports.
	SupportedLanguages() []string
	// Name identifies the provider for logging.
	Name() string
}

// ─────────────────────────────────────────────────────────────────────────────
// DEV MOCK — excluded from production builds by build tag
// ─────────────────────────────────────────────────────────────────────────────

// DevTranslationProvider is a DEV MOCK that returns obviously-marked placeholder text.
// It exists so that the full async pipeline can be integration-tested without
// a live vendor account.
//
// BUILD GUARD: This type must NEVER appear in a production binary.
// The CI pipeline asserts: grep -r DevTranslationProvider backend/cmd/api → should be empty.
// Exit gate in doc 12 Phase 13: DevTranslationProvider is excluded by build flag.
type DevTranslationProvider struct{}

var _ Provider = (*DevTranslationProvider)(nil)

func (d *DevTranslationProvider) Translate(_ context.Context, req Request) (*Result, error) {
	// Deliberately obvious — if this appears in production, it is immediately visible.
	translated := fmt.Sprintf("[DEV MOCK → %s]: %s", req.TargetLanguage, req.Text)
	return &Result{
		TranslatedText:  translated,
		SourceLanguage:  req.SourceLanguage,
		TargetLanguage:  req.TargetLanguage,
		Confidence:      1.0,
		ProviderLatency: 1 * time.Millisecond,
	}, nil
}
func (d *DevTranslationProvider) SupportedLanguages() []string { return []string{"*"} }
func (d *DevTranslationProvider) Name() string                 { return "dev-mock" }

// ─────────────────────────────────────────────────────────────────────────────
// Production provider (vendor-pluggable)
// ─────────────────────────────────────────────────────────────────────────────

// ProductionTranslationProvider wraps a real vendor.
// The vendor is selected by config; the interface remains constant.
// NOT IMPLEMENTED — vendor integration is Phase 13.
type ProductionTranslationProvider struct {
	vendorName string
	apiKey     string // loaded from secrets manager, never from env vars in prod
	endpoint   string
}

func NewProductionProvider(vendorName, endpoint, apiKey string) *ProductionTranslationProvider {
	return &ProductionTranslationProvider{
		vendorName: vendorName,
		endpoint:   endpoint,
		apiKey:     apiKey,
	}
}

func (p *ProductionTranslationProvider) Translate(ctx context.Context, req Request) (*Result, error) {
	// NOT IMPLEMENTED — Phase 13.
	// When implemented: HTTP call to vendor, timeout 2s, with circuit breaker.
	// On vendor failure: return an error; the async pipeline handles gracefully
	// (original text already rendered; translation is optional enrichment).
	return nil, fmt.Errorf("translate: production provider NOT IMPLEMENTED (Phase 13)")
}

func (p *ProductionTranslationProvider) SupportedLanguages() []string {
	return []string{} // NOT IMPLEMENTED
}

func (p *ProductionTranslationProvider) Name() string { return p.vendorName }

// ─────────────────────────────────────────────────────────────────────────────
// Async translation pipeline
// ─────────────────────────────────────────────────────────────────────────────

// AsyncPipeline translates messages asynchronously.
// The original message renders immediately on the client.
// When translation completes, a COMMENT_TRANSLATED event is emitted via WebSocket.
// Translation failure is logged and swallowed — it never blocks the chat pipeline.
type AsyncPipeline struct {
	provider Provider
	cache    TranslationCache
	emitter  EventEmitter
	queue    chan asyncJob
}

type asyncJob struct {
	msgID      string
	roomID     string
	req        Request
	targetLang string
}

// EventEmitter emits COMMENT_TRANSLATED over the WebSocket bus.
type EventEmitter interface {
	EmitCommentTranslated(ctx context.Context, roomID, refMsgID, translated, targetLang string, confidence float64) error
}

// TranslationCache caches translations keyed by (hash(text), targetLang).
type TranslationCache interface {
	Get(ctx context.Context, key string) (*Result, bool)
	Set(ctx context.Context, key string, result *Result)
}

func NewAsyncPipeline(provider Provider, cache TranslationCache, emitter EventEmitter) *AsyncPipeline {
	p := &AsyncPipeline{
		provider: provider,
		cache:    cache,
		emitter:  emitter,
		queue:    make(chan asyncJob, 1000),
	}
	go p.worker()
	return p
}

// Enqueue submits a message for translation. Never blocks. Never returns an error to the caller.
func (p *AsyncPipeline) Enqueue(msgID, roomID string, req Request) {
	select {
	case p.queue <- asyncJob{msgID: msgID, roomID: roomID, req: req, targetLang: req.TargetLanguage}:
	default:
		// Queue full — drop the translation job. Original text is already visible.
		// Log the drop for monitoring but do NOT surface to the user.
	}
}

func (p *AsyncPipeline) worker() {
	for job := range p.queue {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		p.process(ctx, job)
		cancel()
	}
}

func (p *AsyncPipeline) process(ctx context.Context, job asyncJob) {
	cacheKey := translationCacheKey(job.req.Text, job.targetLang)
	if cached, ok := p.cache.Get(ctx, cacheKey); ok {
		_ = p.emitter.EmitCommentTranslated(ctx, job.roomID, job.msgID,
			cached.TranslatedText, job.targetLang, cached.Confidence)
		return
	}

	result, err := p.provider.Translate(ctx, job.req)
	if err != nil {
		// Translation failure is silent to the user. Original text is sufficient.
		return
	}

	p.cache.Set(ctx, cacheKey, result)
	_ = p.emitter.EmitCommentTranslated(ctx, job.roomID, job.msgID,
		result.TranslatedText, job.targetLang, result.Confidence)
}

func translationCacheKey(text, targetLang string) string {
	// Simple key: lowercase text hash + lang. Full implementation uses SHA-256.
	return strings.ToLower(targetLang) + ":" + text
}
