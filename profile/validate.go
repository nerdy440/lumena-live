// Package profile — validation rules for profile fields.
package profile

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var handleRe = regexp.MustCompile(`^[a-zA-Z0-9_]{3,30}$`)

// ValidateHandle checks that a handle meets the requirements.
func ValidateHandle(h string) error {
	if !handleRe.MatchString(h) {
		return ErrInvalidHandle
	}
	return nil
}

// ValidateDisplayName checks length constraints.
func ValidateDisplayName(name string) error {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	if n < 1 || n > 50 {
		return ErrDisplayNameLen
	}
	return nil
}

// SanitizeLanguages deduplicates and normalises language codes.
func SanitizeLanguages(langs []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(langs))
	for _, l := range langs {
		l = strings.ToLower(strings.TrimSpace(l))
		if l != "" && !seen[l] && len(l) <= 8 {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// SanitizeInterests deduplicates and limits to 20.
func SanitizeInterests(interests []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(interests))
	for _, i := range interests {
		i = strings.ToLower(strings.TrimSpace(i))
		if i != "" && !seen[i] && len(i) <= 50 && len(out) < 20 {
			seen[i] = true
			out = append(out, i)
		}
	}
	return out
}
