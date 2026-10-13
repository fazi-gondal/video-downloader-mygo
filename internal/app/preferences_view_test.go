package app

import (
	"reflect"
	"testing"

	"github.com/fazi-gondal/video-downloader-mygo/internal/config"
)

func TestParseSettingsInt(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		min     int
		max     int
		want    int
		wantErr bool
	}{
		{name: "trimmed value", input: " 4 ", min: 1, max: 16, want: 4},
		{name: "lower bound", input: "1", min: 1, max: 16, want: 1},
		{name: "upper bound", input: "16", min: 1, max: 16, want: 16},
		{name: "not a number", input: "many", min: 1, max: 16, wantErr: true},
		{name: "below lower bound", input: "0", min: 1, max: 16, wantErr: true},
		{name: "above upper bound", input: "17", min: 1, max: 16, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSettingsInt(tt.input, "downloads", tt.min, tt.max)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseSettingsInt error = %v, want error: %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("parseSettingsInt = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseSettingsHeaders(t *testing.T) {
	got, err := parseSettingsHeaders("Accept-Language: en-US\n X-Test : one:two\n")
	if err != nil {
		t.Fatalf("parseSettingsHeaders returned error: %v", err)
	}
	want := map[string]string{"Accept-Language": "en-US", "X-Test": "one:two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("headers = %#v, want %#v", got, want)
	}

	for _, input := range []string{"Missing colon", ": value", "Name: ", "Name: one\nName: two"} {
		if _, err := parseSettingsHeaders(input); err == nil {
			t.Errorf("parseSettingsHeaders(%q) returned nil error", input)
		}
	}
}

func TestParseSubtitleLanguages(t *testing.T) {
	got := parseSubtitleLanguages(" en, ur\n en;de ,, ")
	want := []string{"en", "ur", "de"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subtitle languages = %#v, want %#v", got, want)
	}
}

func TestValidateSettingsDraft(t *testing.T) {
	base := config.DefaultSettings()
	tests := []struct {
		name    string
		draft   refinedSettingsDraft
		wantErr bool
	}{
		{
			name: "valid draft",
			draft: refinedSettingsDraft{
				settings:       base,
				maxConcurrent:  "8",
				concurrentFrag: "4",
				rateLimit:      "512",
				headers:        "X-Test: yes",
				subtitleLangs:  "all",
			},
		},
		{
			name: "invalid proxy",
			draft: refinedSettingsDraft{
				settings:       func() config.AppSettings { s := base; s.Proxy = "proxy without scheme"; return s }(),
				maxConcurrent:  "4",
				concurrentFrag: "1",
				rateLimit:      "0",
				subtitleLangs:  "all",
			},
			wantErr: true,
		},
		{
			name: "invalid rate limit",
			draft: refinedSettingsDraft{
				settings:       base,
				maxConcurrent:  "4",
				concurrentFrag: "1",
				rateLimit:      "-1",
				subtitleLangs:  "all",
			},
			wantErr: true,
		},
		{
			name: "invalid cookie browser token",
			draft: refinedSettingsDraft{
				settings: func() config.AppSettings {
					s := base
					s.CookiesBrowser = "chrome profile"
					return s
				}(),
				maxConcurrent:  "4",
				concurrentFrag: "1",
				rateLimit:      "0",
				subtitleLangs:  "all",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateSettingsDraft(tt.draft)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateSettingsDraft error = %v, want error: %v", err, tt.wantErr)
			}
		})
	}
}
