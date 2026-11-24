package i18n

import (
	"encoding/json"
	"regexp"
	"testing"
)

func loadCatalog(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := locales.ReadFile("locales/" + name + ".json")
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var messages map[string]any
	if err := json.Unmarshal(data, &messages); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return messages
}

func TestCatalogsHaveTheSameKeys(t *testing.T) {
	en := loadCatalog(t, "en")
	zh := loadCatalog(t, "zh-Hans")
	for key := range en {
		if key == "@@locale" {
			continue
		}
		if _, ok := zh[key]; !ok {
			t.Errorf("zh-Hans.json is missing %q", key)
		}
	}
	for key := range zh {
		if key == "@@locale" {
			continue
		}
		if _, ok := en[key]; !ok {
			t.Errorf("en.json is missing %q", key)
		}
	}
}

func TestChineseFormatsArguments(t *testing.T) {
	if got, want := T(Chinese, "extractor.selected", map[string]any{"n": 3}), "已选 3 个"; got != want {
		t.Fatalf("T = %q, want %q", got, want)
	}
	if got, want := T(English, "extractor.selected", map[string]any{"n": 3}), "3 selected"; got != want {
		t.Fatalf("T = %q, want %q", got, want)
	}
}

func TestStaticEnglishFallsBackToKey(t *testing.T) {
	if got := T(English, "Analyze", nil); got != "Analyze" {
		t.Fatalf("T = %q, want the key itself", got)
	}
	if got := T(Chinese, "Analyze", nil); got != "解析" {
		t.Fatalf("T = %q, want 解析", got)
	}
}

var semanticKeyRe = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*(\.[a-zA-Z0-9]+)+$`)

// TestEnglishSemanticMessagesAreNotKeys catches placeholder entries where a
// semantic key was copied into its own English message.
func TestEnglishSemanticMessagesAreNotKeys(t *testing.T) {
	en := loadCatalog(t, "en")
	for key, value := range en {
		if !semanticKeyRe.MatchString(key) || key == "@@locale" {
			continue
		}
		if text, ok := value.(string); ok && text == key {
			t.Errorf("en.json has no English message for %q", key)
		}
	}
}
