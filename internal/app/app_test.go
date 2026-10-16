package app

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/fazi-gondal/video-downloader-mygo/internal/config"
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

func TestContainerSelectionLocksToMKVAndRestoresPreference(t *testing.T) {
	a := &App{
		container:          "webm",
		preferredContainer: "webm",
		audioTracksMode:    audioTracksCustom,
		selectedAudio:      map[string]*bool{"en": boolPtr(true), "de": boolPtr(true)},
		subtitleMode:       subtitleModeNone,
		selectedSubtitle:   map[string]*bool{},
	}

	a.syncContainerSelection()
	if a.container != "mkv" {
		t.Fatalf("locked container = %q, want mkv", a.container)
	}
	if req := a.buildRequest("https://example.test/video", "Example", "", "", 0); req.Container != "mkv" {
		t.Fatalf("locked request container = %q, want mkv", req.Container)
	}
	a.audioTracksMode = audioTracksNone
	a.syncContainerSelection()
	if a.container != "webm" {
		t.Fatalf("restored container = %q, want webm", a.container)
	}

	a.subtitleMode = subtitleModeCustom
	a.selectedSubtitle["en"] = boolPtr(true)
	a.syncContainerSelection()
	if a.container != "mkv" {
		t.Fatalf("subtitle-locked container = %q, want mkv", a.container)
	}
	a.subtitleMode = subtitleModeNone
	a.syncContainerSelection()
	if a.container != "webm" {
		t.Fatalf("container after clearing subtitle = %q, want webm", a.container)
	}
}

func TestEstimatedFinalSizeUsesSelectedFormatsAndResolutionFallback(t *testing.T) {
	a := &App{
		currentMedia: &models.MediaInfo{
			Duration: 60,
			Formats: []models.FormatInfo{
				{ID: "video", IsVideo: true, Resolution: "1920x1080", VBR: 800},
				{ID: "audio", IsAudio: true, ABR: 128},
			},
		},
		mode:                models.ModeVideoAudio,
		maxHeight:           1080,
		audioTracksMode:     audioTracksNone,
		selectedVideoFormat: "",
		selectedAudioFormat: "audio",
		subtitleMode:        subtitleModeNone,
		selectedAudio:       map[string]*bool{},
		selectedSubtitle:    map[string]*bool{},
	}

	got, approximate := a.estimatedFinalSize()
	// (800 + 128) kbps for one minute, converted to bytes.
	if got != 6_960_000 {
		t.Fatalf("estimated size = %d, want %d", got, int64(6_960_000))
	}
	if !approximate {
		t.Fatal("bitrate-based estimate should be marked approximate")
	}
}

func TestEstimatedFinalSizeUsesExactAndApproximateFileSizes(t *testing.T) {
	a := &App{
		currentMedia: &models.MediaInfo{
			Duration: 10,
			Formats: []models.FormatInfo{
				{ID: "video", IsVideo: true, Filesize: 1000},
				{ID: "audio", IsAudio: true, FilesizeApprox: 500},
			},
		},
		mode:                models.ModeVideoAudio,
		selectedVideoFormat: "video",
		selectedAudioFormat: "audio",
		audioTracksMode:     audioTracksNone,
		subtitleMode:        subtitleModeNone,
		selectedAudio:       map[string]*bool{},
		selectedSubtitle:    map[string]*bool{},
	}

	got, approximate := a.estimatedFinalSize()
	if got != 1500 || !approximate {
		t.Fatalf("estimated exact/approx size = (%d, %t), want (1500, true)", got, approximate)
	}
}

func TestDownloadMetricsShowsEstimatedTotalSpeedAndETA(t *testing.T) {
	metrics := downloadMetrics(models.ProgressInfo{
		DownloadedBytes: 1024,
		TotalBytes:      4096,
		TotalIsEstimate: true,
		SpeedBps:        2048,
		ETA:             5 * time.Second,
	})
	for _, want := range []string{"25.0%", "1.0 KB / ~4.0 KB", "2.0 KB/s", "ETA 0:05"} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("metrics %q does not contain %q", metrics, want)
		}
	}
}

func TestDownloadMetricsExplainsUnknownProgressFields(t *testing.T) {
	metrics := downloadMetrics(models.ProgressInfo{DownloadedBytes: 1024})
	for _, want := range []string{"1.0 KB / size unknown", "speed calculating", "ETA calculating"} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("metrics %q does not contain %q", metrics, want)
		}
	}
}

func TestBuildRequestCarriesEstimatedSizeForDownloadFallback(t *testing.T) {
	a := &App{
		currentMedia: &models.MediaInfo{
			Duration: 60,
			Formats: []models.FormatInfo{
				{ID: "video", IsVideo: true, Filesize: 2_000_000},
				{ID: "audio", IsAudio: true, FilesizeApprox: 500_000},
			},
		},
		settings:            config.AppSettings{},
		mode:                models.ModeVideoAudio,
		container:           "mp4",
		preferredContainer:  "mp4",
		audioTracksMode:     audioTracksNone,
		subtitleMode:        subtitleModeNone,
		selectedVideoFormat: "video",
		selectedAudioFormat: "audio",
		selectedAudio:       map[string]*bool{},
		selectedSubtitle:    map[string]*bool{},
	}
	req := a.buildRequest("https://example.test/video", "Example", "", "", 0)
	if req.ExpectedSizeBytes != 2_500_000 || !req.ExpectedSizeIsApproximate {
		t.Fatalf("expected download size = (%d, %t), want (2500000, true)", req.ExpectedSizeBytes, req.ExpectedSizeIsApproximate)
	}
}

func TestProgressFractionUsesBytesOrPercentAndProcessingIsIndeterminate(t *testing.T) {
	if got, ok := progressFraction(models.ProgressInfo{DownloadedBytes: 50, TotalBytes: 100}); !ok || got != 0.5 {
		t.Fatalf("byte progress = (%v, %t), want (0.5, true)", got, ok)
	}
	if got, ok := progressFraction(models.ProgressInfo{Percent: 25}); !ok || got != 0.25 {
		t.Fatalf("percent progress = (%v, %t), want (0.25, true)", got, ok)
	}
	if _, ok := progressFraction(models.ProgressInfo{}); ok {
		t.Fatal("empty progress should be indeterminate")
	}
	if _, ok := taskProgressFraction(models.StateProcessing, models.ProgressInfo{Percent: 50}); ok {
		t.Fatal("processing progress should be indeterminate")
	}
}

func TestPlaylistEntryCheckboxCommitsSelection(t *testing.T) {
	a := &App{selectedEntries: map[int]bool{}}
	tester := ui.NewTester(func(c *ui.Context) {
		a.playlistEntryCheckbox(c, 0)
	}, 240, 48)
	if err := tester.Click("playlist-entry-0"); err != nil {
		t.Fatal(err)
	}
	if !a.selectedEntries[0] {
		t.Fatal("playlist checkbox click did not persist the selected entry")
	}
}
