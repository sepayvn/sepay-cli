// Package locale picks the language of the few screens the CLI translates.
package locale

import "strings"

// Lang is a supported display language.
type Lang string

// The supported languages; EN is the fallback.
const (
	EN Lang = "en"
	VI Lang = "vi"
)

// Detect reads SEPAY_LANG, then the POSIX locale variables in their precedence order; on Windows
// it falls back to the user's UI language. Anything that is not Vietnamese reads as English.
func Detect(getenv func(string) string) Lang {
	switch strings.ToLower(strings.TrimSpace(getenv("SEPAY_LANG"))) {
	case "vi":
		return VI
	case "en":
		return EN
	}
	// The first locale variable set decides, as setlocale does: LANG=vi_VN under LC_ALL=C is English.
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := getenv(key); v != "" {
			return fromTag(v)
		}
	}
	return fromTag(systemLanguage())
}

// fromTag reads a POSIX locale (vi_VN.UTF-8) or a BCP 47 tag (vi-VN).
func fromTag(tag string) Lang {
	tag = strings.ToLower(tag)
	if tag == "vi" || strings.HasPrefix(tag, "vi_") || strings.HasPrefix(tag, "vi-") || strings.HasPrefix(tag, "vi.") {
		return VI
	}
	return EN
}
