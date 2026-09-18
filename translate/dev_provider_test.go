//go:build !production

package translate_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lumena/translate"
)

func TestDevProvider_ReturnsMockPrefixedText(t *testing.T) {
	p := translate.NewDevProvider()
	result, err := p.Translate(context.Background(), "hello world", "en", "ar")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.TranslatedBody, "[MOCK: ") {
		t.Fatalf("expected mock-prefixed translation making it unmistakably fake, got %q", result.TranslatedBody)
	}
	if !strings.Contains(result.TranslatedBody, "hello world") {
		t.Fatalf("expected the original text preserved inside the mock marker, got %q", result.TranslatedBody)
	}
	if result.TargetLang != "ar" {
		t.Fatalf("expected target_lang echoed back as ar, got %q", result.TargetLang)
	}
}

func TestDevProvider_NeverErrors(t *testing.T) {
	// The dev provider must never fail — tests exercising the "failure
	// doesn't block delivery" path elsewhere need a way to simulate that
	// separately (a fake Provider), not by relying on this one to break.
	p := translate.NewDevProvider()
	_, err := p.Translate(context.Background(), "", "", "")
	if err != nil {
		t.Fatalf("expected no error even for empty input, got %v", err)
	}
}
