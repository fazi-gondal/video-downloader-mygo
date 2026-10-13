package app

import (
	"reflect"
	"testing"

	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

func TestSubtitleLanguagesPrefersAvailableCreatorTracks(t *testing.T) {
	media := &models.MediaInfo{
		Subtitles: map[string][]models.SubtitleTrack{
			"en": {},
			"de": {{Lang: "de"}},
			"hi": {{Lang: "hi"}},
		},
		AutomaticCaptions: map[string][]models.SubtitleTrack{
			"en":        {{Lang: "en"}},
			"fr":        {},
			"de":        {{Lang: "de"}},
			"live_chat": {{Lang: "live_chat"}},
		},
	}

	want := []string{"de", "hi"}
	if got := subtitleLanguages(media); !reflect.DeepEqual(got, want) {
		t.Fatalf("subtitle languages = %#v, want %#v", got, want)
	}
}

func TestSubtitleLanguagesFallsBackToAvailableAutomaticCaptions(t *testing.T) {
	media := &models.MediaInfo{
		AutomaticCaptions: map[string][]models.SubtitleTrack{
			"en":        {{Lang: "en"}},
			"de":        {},
			"hi":        {{Lang: "hi"}},
			"live_chat": {{Lang: "live_chat"}},
		},
	}

	want := []string{"en", "hi"}
	if got := subtitleLanguages(media); !reflect.DeepEqual(got, want) {
		t.Fatalf("automatic subtitle languages = %#v, want %#v", got, want)
	}
}

func TestLanguageDisplayNameUsesCompleteNames(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{code: "en", want: "English"},
		{code: "hi", want: "Hindi"},
		{code: "de", want: "German"},
		{code: "en-US", want: "English"},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			if got := languageDisplayName(tt.code); got != tt.want {
				t.Fatalf("languageDisplayName(%q) = %q, want %q", tt.code, got, tt.want)
			}
		})
	}
}

func TestBuildRequestPreservesSubtitleCodesForYTDLP(t *testing.T) {
	a := &App{
		subtitleMode: subtitleModeCustom,
		selectedSubtitle: map[string]*bool{
			"hi": boolPtr(true),
			"de": boolPtr(true),
			"en": boolPtr(false),
		},
	}

	req := a.buildRequest("https://example.test/video", "Example", "", "", 0)
	if req.SubtitleSelectionMode != models.SubtitleSelectionCustom {
		t.Fatalf("subtitle selection mode = %q, want %q", req.SubtitleSelectionMode, models.SubtitleSelectionCustom)
	}
	want := []string{"de", "hi"}
	if !reflect.DeepEqual(req.SubtitleLangs, want) {
		t.Fatalf("subtitle language codes = %#v, want %#v", req.SubtitleLangs, want)
	}
}
