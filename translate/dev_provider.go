//go:build !production

// DevProvider is compiled in by default (no build tag needed to get it) —
// see production_provider.go for the inverse. This split is the actual
// mechanism behind the roadmap Phase 13 exit gate: "DevTranslationProvider
// is excluded from production build by build flag." Building with
// `-tags production` swaps this file out entirely; it cannot leak into a
// production binary by a runtime misconfiguration, only by someone
// deliberately omitting the tag.
package translate

import (
	"context"
	"fmt"
)

// DevProvider is a deterministic mock — DEV ONLY, NEVER SHIPS. It returns
// "[MOCK: <original>]" exactly as the roadmap specifies, so a translated
// bubble is visibly, unmistakably fake in any screenshot or demo — nobody
// could confuse it for a real translation.
type DevProvider struct{}

func NewDevProvider() *DevProvider { return &DevProvider{} }

func (p *DevProvider) Translate(_ context.Context, body, sourceLang, targetLang string) (*Result, error) {
	return &Result{
		TranslatedBody: fmt.Sprintf("[MOCK: %s]", body),
		TargetLang:     targetLang,
		Confidence:     0.99,
	}, nil
}

// New returns whichever Provider this binary was built with — callers
// (main.go) never choose between Dev/Production themselves, so there's no
// runtime code path that could pick the mock in a production build.
func New() Provider { return NewDevProvider() }
