// Package translate implements async chat translation (doc 08 §5,
// roadmap Phase 13). The Provider interface is deliberately the only
// thing callers depend on — which concrete implementation gets linked in
// is a build-time decision (see dev_provider.go / production_provider.go),
// not a runtime one, so a dev mock can never accidentally ship.
package translate

import (
	"context"
	"errors"
)

// Result is one translation outcome.
type Result struct {
	TranslatedBody string
	TargetLang     string
	Confidence     float64
}

// Provider translates text from sourceLang to targetLang. Implementations
// must never block the caller's chat pipeline — see doc 08 §5: "the
// original comment renders immediately and the translation arrives
// asynchronously." Callers are expected to invoke Translate off the
// message-delivery path (e.g. in a goroutine) and treat any error as
// "no translation available," never as a reason to delay or drop the
// original message.
type Provider interface {
	Translate(ctx context.Context, body, sourceLang, targetLang string) (*Result, error)
}

// ErrNotConfigured is returned by ProductionProvider until a real vendor
// integration is wired in — see production_provider.go's doc comment.
var ErrNotConfigured = errors.New("translate: no production translation vendor configured")
