//go:build production

// Built only with `-tags production` — see dev_provider.go's doc comment
// for the other half of the exit gate this enforces.
package translate

import "context"

// ProductionProvider is where a real vendor integration (Google Translate,
// DeepL, AWS Translate, etc.) would live. None is wired in here — this
// repo has no vendor credentials or contract, so ProductionProvider
// honestly reports that rather than pretending to translate. Standing this
// up for real means implementing Translate against a chosen vendor's SDK
// and injecting its API key via config, not code in this package.
type ProductionProvider struct{}

func NewProductionProvider() *ProductionProvider { return &ProductionProvider{} }

func (p *ProductionProvider) Translate(_ context.Context, _, _, _ string) (*Result, error) {
	return nil, ErrNotConfigured
}

// New returns whichever Provider this binary was built with — see
// dev_provider.go's New for the other half of this build-tag pair.
func New() Provider { return NewProductionProvider() }
