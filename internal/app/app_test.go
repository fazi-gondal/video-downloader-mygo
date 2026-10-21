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

func TestSubtitleLanguagesExcludesAutomaticCaptionsWhenNoCreatorSubtitlesExist(t *testing.T) {
	media := &models.MediaInfo{
		AutomaticCaptions: map[string][]models.SubtitleTrack{
			"en":        {{Lang: "en"}},
			"de":        {},
			"hi":        {{Lang: "hi"}},
			"live_chat": {{Lang: "live_chat"}},
		},
	}

	if got := subtitleLanguages(media); len(got) != 0 {
		t.Fatalf("automatic subtitle languages = %#v, want none", got)
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

func TestBuildRequestUsesSettingsVP9Preference(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "enabled", want: true},
		{name: "fallback disabled", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &App{
				settings:           config.AppSettings{PreferVP9Video: tt.want},
				settingsDraftReady: false,
				mode:               models.ModeVideoAudio,
				container:          "mp4",
				preferredContainer: "mp4",
				audioTracksMode:    audioTracksNone,
				subtitleMode:       subtitleModeNone,
				selectedAudio:      map[string]*bool{},
				selectedSubtitle:   map[string]*bool{},
			}

			req := a.buildRequest("https://www.youtube.com/watch?v=example", "Example", "", "", 0)
			if req.PreferVP9 != tt.want {
				t.Fatalf("request PreferVP9 = %t, want %t", req.PreferVP9, tt.want)
			}
		})
	}
}

func TestBuildRequestCarries360VideoFlag(t *testing.T) {
	a := &App{
		currentMedia:       &models.MediaInfo{Is360: true},
		mode:               models.ModeVideoAudio,
		container:          "mkv",
		preferredContainer: "mkv",
		audioTracksMode:    audioTracksNone,
		subtitleMode:       subtitleModeNone,
		selectedAudio:      map[string]*bool{},
		selectedSubtitle:   map[string]*bool{},
	}

	if req := a.buildRequest("https://www.youtube.com/watch?v=example", "Example", "", "", 0); !req.Is360 {
		t.Fatal("360 media flag was not copied to the download request")
	}
}

func TestRebuildFormatOptionsHidesMeshStreamsFor360Video(t *testing.T) {
	a := &App{
		currentMedia: &models.MediaInfo{
			Is360: true,
			Formats: []models.FormatInfo{
				{ID: "248", IsVideo: true, Note: "1080s, mesh", Height: 1080, VCodec: "vp9"},
				{ID: "614", IsVideo: true, Protocol: "m3u8_native", Height: 1080, VCodec: "vp09"},
			},
		},
	}

	a.rebuildFormatOptions()
	if strings.Contains(strings.Join(a.videoFmtIDs, ","), "248") {
		t.Fatalf("mesh video format remained available: %#v", a.videoFmtIDs)
	}
	if !strings.Contains(strings.Join(a.videoFmtIDs, ","), "614") {
		t.Fatalf("equirectangular HLS video format was removed: %#v", a.videoFmtIDs)
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

func TestEstimatedFinalSizeDoesNotDoubleCountMuxedFallbackAudio(t *testing.T) {
	a := &App{
		currentMedia: &models.MediaInfo{
			Duration: 60,
			Formats: []models.FormatInfo{
				{ID: "muxed", Ext: "mp4", IsVideo: true, IsAudio: true, Filesize: 10_000_000, Height: 1080},
				{ID: "audio", Ext: "m4a", IsAudio: true, Filesize: 2_000_000, ABR: 128},
			},
		},
		mode:             models.ModeVideoAudio,
		maxHeight:        1080,
		container:        "mp4",
		audioTracksMode:  audioTracksNone,
		subtitleMode:     subtitleModeNone,
		selectedAudio:    map[string]*bool{},
		selectedSubtitle: map[string]*bool{},
	}
	got, _ := a.estimatedFinalSize()
	if got != 10_000_000 {
		t.Fatalf("muxed fallback estimate = %d, want 10000000 without duplicate audio", got)
	}
}

func TestEstimatedFinalSizeAddsOnlySelectedAudioTracks(t *testing.T) {
	on := true
	a := &App{
		currentMedia: &models.MediaInfo{
			Duration: 60,
			Formats: []models.FormatInfo{
				{ID: "video", Ext: "mp4", IsVideo: true, Filesize: 8_000_000, Height: 1080},
				{ID: "en", Ext: "m4a", IsAudio: true, Filesize: 1_000_000, ABR: 128},
				{ID: "de", Ext: "m4a", IsAudio: true, Filesize: 1_500_000, ABR: 192},
				{ID: "es", Ext: "m4a", IsAudio: true, Filesize: 2_000_000, ABR: 256},
			},
			AudioTracks: []models.AudioTrack{
				{ID: "en", Language: "en", Bitrate: 128},
				{ID: "de", Language: "de", Bitrate: 192},
				{ID: "es", Language: "es", Bitrate: 256},
			},
		},
		mode:             models.ModeVideoAudio,
		maxHeight:        1080,
		container:        "mp4",
		audioTracksMode:  audioTracksCustom,
		selectedAudio:    map[string]*bool{"en": &on, "de": &on},
		subtitleMode:     subtitleModeNone,
		selectedSubtitle: map[string]*bool{},
	}
	got, _ := a.estimatedFinalSize()
	if got != 10_500_000 {
		t.Fatalf("custom multi-audio estimate = %d, want 10500000", got)
	}
}

func TestEstimatedFinalSizeUsesSeparateVideoAndAudioStreams(t *testing.T) {
	a := &App{
		currentMedia: &models.MediaInfo{
			Duration: 60,
			Formats: []models.FormatInfo{
				{ID: "video", Ext: "mp4", IsVideo: true, Filesize: 8_000_000, Height: 1080},
				{ID: "audio", Ext: "m4a", IsAudio: true, Filesize: 2_000_000, ABR: 128},
			},
		},
		mode:             models.ModeVideoAudio,
		maxHeight:        1080,
		container:        "mp4",
		audioTracksMode:  audioTracksNone,
		subtitleMode:     subtitleModeNone,
		selectedAudio:    map[string]*bool{},
		selectedSubtitle: map[string]*bool{},
	}
	got, _ := a.estimatedFinalSize()
	if got != 10_000_000 {
		t.Fatalf("separate stream estimate = %d, want 10000000", got)
	}
}

func TestEstimatedFinalSizeAudioOnlyUsesSelectedAudio(t *testing.T) {
	a := &App{
		currentMedia: &models.MediaInfo{
			Duration: 60,
			Formats: []models.FormatInfo{
				{ID: "audio", Ext: "m4a", IsAudio: true, Filesize: 2_000_000},
			},
		},
		mode:                models.ModeAudioOnly,
		selectedAudioFormat: "audio",
		selectedAudio:       map[string]*bool{},
		selectedSubtitle:    map[string]*bool{},
	}
	got, _ := a.estimatedFinalSize()
	if got != 2_000_000 {
		t.Fatalf("audio-only estimate = %d, want 2000000", got)
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

func TestSubtitleLanguagesFiltersUndeterminedAndUnknown(t *testing.T) {
	media := &models.MediaInfo{
		Subtitles: map[string][]models.SubtitleTrack{
			"und":       {{Lang: "und"}},
			"unknown":   {{Lang: "unknown"}},
			"none":      {{Lang: "none"}},
			"live_chat": {{Lang: "live_chat"}},
			"en":        {{Lang: "en"}},
		},
	}
	got := subtitleLanguages(media)
	want := []string{"en"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subtitleLanguages() = %#v, want %#v", got, want)
	}
}

func TestHasSubtitlesReturnsFalseWhenNoValidSubtitles(t *testing.T) {
	a := &App{}
	if a.hasSubtitles() {
		t.Fatal("hasSubtitles() should be false when currentMedia is nil")
	}

	a.currentMedia = &models.MediaInfo{
		Subtitles: map[string][]models.SubtitleTrack{},
	}
	if a.hasSubtitles() {
		t.Fatal("hasSubtitles() should be false when Subtitles is empty")
	}

	a.currentMedia.Subtitles["und"] = []models.SubtitleTrack{{Lang: "und"}}
	if a.hasSubtitles() {
		t.Fatal("hasSubtitles() should be false when Subtitles only contains 'und'")
	}

	a.currentMedia.Subtitles["en"] = []models.SubtitleTrack{{Lang: "en"}}
	if !a.hasSubtitles() {
		t.Fatal("hasSubtitles() should be true when valid subtitles exist")
	}
}

func TestAudioTrackLanguageFormatting(t *testing.T) {
	a := &App{}
	tr := models.AudioTrack{
		ID:       "251",
		Language: "en",
		Codec:    "opus",
		Bitrate:  128,
		Note:     "medium",
	}

	lang := a.audioTrackLanguage(tr)
	if lang != "English" {
		t.Fatalf("audioTrackLanguage() = %q, want %q", lang, "English")
	}
}

func TestHighQualityOpusAudioTracks(t *testing.T) {
	// Single track should be returned as-is
	single := []models.AudioTrack{
		{ID: "251-en", Language: "en", Codec: "opus", Bitrate: 160},
	}
	if got := highQualityOpusAudioTracks(single); len(got) != 1 || got[0].ID != "251-en" {
		t.Fatalf("single track = %#v, want 1 track", got)
	}

	// Multiple tracks with Opus preferred per-language, but non-Opus language (ja) not dropped
	tracks := []models.AudioTrack{
		{ID: "140-en", Language: "en", Codec: "mp4a.40.2", Bitrate: 128},
		{ID: "250-en", Language: "en", Codec: "opus", Bitrate: 70},
		{ID: "251-en", Language: "en", Codec: "opus", Bitrate: 160},
		{ID: "140-hi", Language: "hi", Codec: "mp4a.40.2", Bitrate: 128},
		{ID: "251-hi", Language: "hi", Codec: "opus", Bitrate: 160},
		{ID: "140-ja", Language: "ja", Codec: "mp4a.40.2", Bitrate: 128}, // Only AAC available for Japanese
	}

	best := highQualityOpusAudioTracks(tracks)
	if len(best) != 3 {
		t.Fatalf("len(best) = %d, want 3", len(best))
	}
	wantIDs := []string{"251-en", "251-hi", "140-ja"}
	var gotIDs []string
	for _, tr := range best {
		gotIDs = append(gotIDs, tr.ID)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("highQualityOpusAudioTracks() IDs = %#v, want %#v", gotIDs, wantIDs)
	}
}

func TestSortSelectedAudioTrackIDsOriginalFirst(t *testing.T) {
	a := &App{
		currentMedia: &models.MediaInfo{
			AudioTracks: []models.AudioTrack{
				{ID: "track-ar", Language: "ar", Note: "Arabic - dubbed"},
				{ID: "track-es", Language: "es", Note: "Spanish - dubbed"},
				{ID: "track-de", Language: "de", Note: "German - dubbed"},
				{ID: "track-orig", Language: "en", Note: "English - original (original)", IsOriginal: true},
			},
		},
	}

	// 1. When original is selected along with others, original is main (first) and others are A-Z
	selected := []string{"track-es", "track-orig", "track-de", "track-ar"}
	got := a.sortSelectedAudioTrackIDs(selected)
	// Display names: Arabic ("track-ar"), German ("track-de"), Spanish ("track-es")
	want := []string{"track-orig", "track-ar", "track-de", "track-es"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sortSelectedAudioTrackIDs() with original = %#v, want %#v", got, want)
	}

	// 2. When original is NOT selected, use A to Z format by language display name
	selectedNoOrig := []string{"track-es", "track-de", "track-ar"}
	gotNoOrig := a.sortSelectedAudioTrackIDs(selectedNoOrig)
	wantNoOrig := []string{"track-ar", "track-de", "track-es"}
	if !reflect.DeepEqual(gotNoOrig, wantNoOrig) {
		t.Fatalf("sortSelectedAudioTrackIDs() without original = %#v, want %#v", gotNoOrig, wantNoOrig)
	}
}

func TestSortSelectedSubtitleLangsEnglishFirst(t *testing.T) {
	// 1. When English is selected along with others, English is main (first) and others are A-Z
	selected := []string{"es", "en", "de", "ar"}
	got := sortSelectedSubtitleLangs(selected)
	// Display names: Arabic ("ar"), German ("de"), Spanish ("es")
	want := []string{"en", "ar", "de", "es"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sortSelectedSubtitleLangs() with English = %#v, want %#v", got, want)
	}

	// 2. When English is NOT selected, use A to Z format by language display name
	selectedNoEn := []string{"es", "de", "ar"}
	gotNoEn := sortSelectedSubtitleLangs(selectedNoEn)
	wantNoEn := []string{"ar", "de", "es"}
	if !reflect.DeepEqual(gotNoEn, wantNoEn) {
		t.Fatalf("sortSelectedSubtitleLangs() without English = %#v, want %#v", gotNoEn, wantNoEn)
	}
}

func TestAudioTrackLanguageFormattingOriginal(t *testing.T) {
	a := &App{}
	origTrack := models.AudioTrack{
		ID:       "251",
		Language: "en",
		Codec:    "opus",
		Bitrate:  128,
		Note:     "original",
	}

	lang := a.audioTrackLanguage(origTrack)
	if lang != "English (Original)" {
		t.Fatalf("audioTrackLanguage() = %q, want %q", lang, "English (Original)")
	}
}

func TestHighQualityOpusAudioTracksPrioritizesOriginal(t *testing.T) {
	tracks := []models.AudioTrack{
		{ID: "251-ar", Language: "ar", Codec: "opus", Bitrate: 128},
		{ID: "251-de", Language: "de", Codec: "opus", Bitrate: 128},
		{ID: "251-en", Language: "en", Codec: "opus", Bitrate: 128, IsOriginal: true},
	}

	best := highQualityOpusAudioTracks(tracks)
	if len(best) != 3 {
		t.Fatalf("len(best) = %d, want 3", len(best))
	}
	if best[0].ID != "251-en" {
		t.Fatalf("best[0].ID = %q, want original track 251-en first", best[0].ID)
	}
	if best[1].ID != "251-ar" || best[2].ID != "251-de" {
		t.Fatalf("remaining tracks = [%q, %q], want [251-ar, 251-de]", best[1].ID, best[2].ID)
	}
}
