// Package i18n loads the app's message catalogs through MyGo's native i18n
// (ICU messages, locale matching, plural rules) and exposes a small lookup
// helper. English is the source language; static UI strings use their English
// text as the key, dynamic strings use semantic keys listed in en.json.
package i18n

import (
	"embed"
	"io/fs"
	"strings"

	mygoi18n "github.com/egoist/mygo/i18n"
)

//go:embed locales/*.json
var locales embed.FS

const (
	English = "en"
	Chinese = "zh-Hans"
)

var catalog *mygoi18n.Catalog

func init() {
	root, err := fs.Sub(locales, "locales")
	if err != nil {
		return
	}
	c, err := mygoi18n.NewCatalog(root, English)
	if err == nil {
		catalog = c
	}
}

// Detect maps a BCP 47 tag to a supported language.
func Detect(locale string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "zh") {
		return Chinese
	}
	return English
}

// T formats key in lang. A missing message falls back to the key itself, so
// English static strings need no catalog entry.
func T(lang, key string, args map[string]any) string {
	if catalog == nil {
		return key
	}
	return catalog.Format(lang, key, args)
}

// Has reports whether the catalog has a message for key in lang.
func Has(lang, key string) bool {
	if catalog == nil {
		return false
	}
	_, _, ok := catalog.Message(lang, key)
	return ok
}

// Count renders a count with the singular or plural message for lang.
func Count(lang string, n int, singularKey, pluralKey string) string {
	if n == 1 {
		return T(lang, singularKey, nil)
	}
	return T(lang, pluralKey, map[string]any{"n": n})
}
