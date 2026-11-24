package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/fazi-gondal/video-downloader-mygo/internal/i18n"
)

var (
	trKeyRe    = regexp.MustCompile(`a\.tr\("((?:[^"\\]|\\.)*)"`)
	countKeyRe = regexp.MustCompile(`i18n\.Count\([^,]+,\s*[^,]+,\s*"((?:[^"\\]|\\.)*)",\s*"((?:[^"\\]|\\.)*)"`)
)

// TestTranslationKeysExist scans the UI sources for every catalog key and
// requires both languages to have a message for it. It catches a key added to
// one catalog only, which would otherwise render literally in the other.
func TestTranslationKeysExist(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range trKeyRe.FindAllStringSubmatch(string(source), -1) {
			used[m[1]] = true
		}
		for _, m := range countKeyRe.FindAllStringSubmatch(string(source), -1) {
			used[m[1]] = true
			used[m[2]] = true
		}
	}
	for key := range used {
		for _, lang := range []string{i18n.English, i18n.Chinese} {
			if !i18n.Has(lang, key) {
				t.Errorf("catalog %q has no message for %q", lang, key)
			}
		}
	}
}
