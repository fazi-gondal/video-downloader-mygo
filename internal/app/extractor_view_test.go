package app

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/fazi-gondal/video-downloader-mygo/internal/config"
	"github.com/fazi-gondal/video-downloader-mygo/internal/i18n"
	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
	"github.com/fazi-gondal/video-downloader-mygo/internal/services"
	appui "github.com/fazi-gondal/video-downloader-mygo/internal/ui"
)

func testerView(a *App) func(c *ui.Context) {
	return func(c *ui.Context) {
		a.lang = i18n.Detect(c.Locale())
		a.extractOpen = true
		c.SetTheme(appui.NativeTheme(true))
		ui.Column(c).Grow(1).Padding(16).Children(func() {
			a.extractorCollapsible(c, 720)
		})
	}
}

func sampleExtractResult() *services.SiteExtractResult {
	return &services.SiteExtractResult{
		PageURL: "https://example.com/archives/1/",
		Title:   "Resolved example page",
		Videos: []services.SiteVideo{
			{ID: "1", Title: "Part one", MediaURL: "https://hls.example.com/videos5/aaa/aaa.m3u8?auth_key=1-2-3&v=3", Format: "hls"},
			{ID: "2", Title: "Part two", MediaURL: "https://hls.example.com/videos5/bbb/bbb.m3u8?auth_key=4-5-6&v=3", Format: "hls"},
		},
	}
}

func sampleExtractApp() *App {
	a := &App{}
	a.extractResult = sampleExtractResult()
	a.extractItems = []extractItem{
		{video: a.extractResult.Videos[0], media: &models.MediaInfo{Title: "Part one", Duration: 2896, Formats: []models.FormatInfo{{Height: 1080}}}},
		{video: a.extractResult.Videos[1], media: &models.MediaInfo{Title: "Part two", Duration: 640, Formats: []models.FormatInfo{{Height: 720}}}},
	}
	a.extractSelected = map[int]bool{0: true, 1: true}
	return a
}

func TestExtractorPageRendersInput(t *testing.T) {
	a := &App{}
	tt := ui.NewTester(testerView(a), 900, 620)
	tt.Frame()

	if !tt.HasText("Hide page extractor") {
		t.Fatal("extractor trigger missing")
	}
	if !tt.HasText("Resolve") {
		t.Fatal("Resolve button missing")
	}
	if !tt.HasText("player configs") {
		t.Fatal("explanatory hint missing")
	}
}

func TestExtractorPageIsLocalized(t *testing.T) {
	a := &App{}
	tt := ui.NewTester(testerView(a), 900, 620)
	tt.SetLocale("zh-CN")
	tt.Frame()

	if !tt.HasText("网页提取") {
		t.Fatalf("Chinese trigger missing; texts: %#v", tt.Texts())
	}
	if !tt.HasText("解析") {
		t.Fatal("Chinese resolve button missing")
	}
}

func TestExtractorPageRejectsEmptyInput(t *testing.T) {
	a := &App{}
	tt := ui.NewTester(testerView(a), 900, 620)
	tt.Frame()

	if err := tt.Click("Resolve"); err != nil {
		t.Fatalf("click Resolve: %v", err)
	}
	tt.Frame()
	if !tt.HasText("Paste a page URL first.") {
		t.Fatalf("missing empty-input error; texts: %#v", tt.Texts())
	}
}

func TestExtractorPageListsEveryVideo(t *testing.T) {
	a := sampleExtractApp()
	tt := ui.NewTester(testerView(a), 900, 620)
	tt.Frame()

	if !tt.HasText("Resolved example page") {
		t.Fatal("result title missing")
	}
	if !tt.HasText("2 videos · example.com") {
		t.Fatal("result metadata missing")
	}
	if !tt.HasText("Part one") || !tt.HasText("Part two") {
		t.Fatal("both video rows missing")
	}
	if !tt.HasText("48:16") || !tt.HasText("1080p") {
		t.Fatalf("probed metadata missing; texts: %#v", tt.Texts())
	}
	if !tt.HasText("2 selected") {
		t.Fatal("selection count missing")
	}
	if !tt.HasText("Continue with 2") {
		t.Fatal("continue button missing")
	}
}

func TestContinueExtractBuildsPlaylist(t *testing.T) {
	a := sampleExtractApp()
	a.continueExtract()

	if a.page != PageConfig {
		t.Fatalf("page = %q, want %q", a.page, PageConfig)
	}
	if a.currentPlaylist == nil {
		t.Fatal("playlist not built")
	}
	if got := len(a.currentPlaylist.Entries); got != 2 {
		t.Fatalf("entries = %d, want 2", got)
	}
	if !a.selectedEntries[0] || !a.selectedEntries[1] {
		t.Fatal("playlist entries should be preselected")
	}
	if a.currentPlaylist.Entries[0].Duration != 2896 {
		t.Fatalf("duration = %v", a.currentPlaylist.Entries[0].Duration)
	}
}

func TestContinueExtractUsesSingleMedia(t *testing.T) {
	a := sampleExtractApp()
	a.extractSelected = map[int]bool{1: true}
	a.continueExtract()

	if a.page != PageConfig {
		t.Fatalf("page = %q, want %q", a.page, PageConfig)
	}
	if a.currentMedia == nil || a.currentMedia.Title != "Part two" {
		t.Fatalf("currentMedia = %#v", a.currentMedia)
	}
	if a.currentPlaylist != nil {
		t.Fatal("single selection should not build a playlist")
	}
}

func TestContinueExtractRequiresSelection(t *testing.T) {
	a := sampleExtractApp()
	a.extractSelected = map[int]bool{}
	a.continueExtract()
	if a.page == PageConfig {
		t.Fatal("should not continue without a selection")
	}
}

func TestExtractorPageSnapshot(t *testing.T) {
	dir := os.Getenv("UI_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set UI_SNAPSHOT_DIR to write a render snapshot")
	}
	a := sampleExtractApp()
	tt := ui.NewTester(testerView(a), 900, 560)
	tt.SetLocale("zh-CN")
	tt.Frame()

	path := filepath.Join(dir, "extractor-page.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	t.Logf("snapshot: %s", path)
}

func TestPreferredLocaleHonorsSetting(t *testing.T) {
	a := &App{}
	a.settingsDraft.Language = "zh-Hans"
	if got := a.preferredLocale(); got != "zh-Hans" {
		t.Fatalf("locale = %q, want zh-Hans", got)
	}
	a.settingsDraft.Language = "en"
	if got := a.preferredLocale(); got != "en" {
		t.Fatalf("locale = %q, want en", got)
	}
}

func TestFriendlyErrorMapsFFmpegFailure(t *testing.T) {
	a := &App{lang: "zh-Hans"}
	got := a.friendlyError("ERROR: Postprocessing: ffmpeg not found. Please install or provide the path using --ffmpeg-location")
	if !strings.Contains(got, "FFmpeg") {
		t.Fatalf("friendly error = %q", got)
	}
	dyld := a.friendlyError("dyld: Library not loaded: /opt/homebrew/opt/x265/lib/libx265.dylib")
	if !strings.Contains(dyld, "FFmpeg") {
		t.Fatalf("dyld error = %q", dyld)
	}
	missing := a.friendlyError(`start yt-dlp: exec: "yt-dlp": executable file not found in $PATH`)
	if !strings.Contains(missing, "yt-dlp") {
		t.Fatalf("missing yt-dlp error = %q", missing)
	}
	aryKey := a.friendlyError("ARY Plus API key is not configured; add it in Settings → ARY Plus")
	if !strings.Contains(aryKey, "ARY Plus") {
		t.Fatalf("missing ARY Plus key error = %q", aryKey)
	}
	ambiguous := a.friendlyError("yt-dlp finished without reporting a single final media file: 2 media files in staging directory")
	if !strings.Contains(ambiguous, "最终文件") {
		t.Fatalf("ambiguous output error = %q", ambiguous)
	}
	if !strings.Contains(a.friendlyError("ERROR: Unsupported URL: https://example.com"), "网页提取") {
		t.Fatalf("unsupported error not mapped")
	}
}

func TestHistoryDownloadRequestCarriesSourcePage(t *testing.T) {
	entry := services.HistoryEntry{
		URL: "https://cdn.example.com/a.m3u8", Title: "Part two",
		SourcePageURL: "https://example.com/archives/1/", SourceIndex: 1,
	}
	req := historyDownloadRequest(entry, "/tmp")
	if req.SourcePageURL != entry.SourcePageURL || req.SourceIndex != entry.SourceIndex {
		t.Fatalf("source = %q/%d, want %q/%d", req.SourcePageURL, req.SourceIndex, entry.SourcePageURL, entry.SourceIndex)
	}
}

func TestFriendlyErrorMapsHTTP429(t *testing.T) {
	a := &App{lang: "zh-Hans"}
	if got := a.friendlyError("ERROR: HTTP 429: Too Many Requests"); !strings.Contains(got, "限流") {
		t.Fatalf("friendly error = %q", got)
	}
}

func TestLanguageOnlySettingsLeavesOtherFields(t *testing.T) {
	settings := config.DefaultSettings()
	settings.DownloadDir = ""
	got := languageOnlySettings(settings, "zh-Hans")
	if got.Language != "zh-Hans" {
		t.Fatalf("language = %q", got.Language)
	}
	if got.DownloadDir != "" || got.MaxConcurrent != settings.MaxConcurrent {
		t.Fatalf("other fields changed: %#v", got)
	}
}
