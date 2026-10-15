package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/config"
	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_YTDLP_HELPER") == "1" {
		if os.Getenv("GO_WANT_YTDLP_HELPER_BLOCK") == "1" {
			fmt.Fprintln(os.Stdout, "download:0|1|1|NA|NA|0|0")
			for {
				fmt.Fprintln(os.Stderr, strings.Repeat("diagnostic ", 512))
				time.Sleep(10 * time.Millisecond)
			}
		}
		fmt.Fprintln(os.Stdout, "download:1|1|1|NA|NA|1|1")
		fmt.Fprintln(os.Stderr, strings.Repeat("diagnostic ", 512))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestYtDlpHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_YTDLP_HELPER") != "1" {
		return
	}
	t.Fatal("helper process must be handled by TestMain")
}

func TestBoundedDiagnosticBufferRetainsTailWithinLimit(t *testing.T) {
	var diagnostics boundedDiagnosticBuffer
	diagnostics.limit = 8
	if _, err := diagnostics.Write([]byte("old output")); err != nil {
		t.Fatal(err)
	}
	if _, err := diagnostics.Write([]byte("ERROR: final failure")); err != nil {
		t.Fatal(err)
	}
	if len(diagnostics.String()) > diagnostics.limit+len("\n[yt-dlp diagnostics truncated]") {
		t.Fatalf("bounded diagnostics length = %d, exceeds limit", len(diagnostics.String()))
	}
	if !strings.Contains(diagnostics.String(), "failure") {
		t.Fatalf("bounded diagnostics = %q, want final diagnostic", diagnostics.String())
	}
}

func testYtDlpHelperService(t *testing.T) *YtDlpService {
	t.Helper()
	return &YtDlpService{
		ytdlpPath: os.Args[0],
		settings:  func() config.AppSettings { return config.AppSettings{} },
	}
}

func TestYtDlpDownloadDrainsStdoutAndStderrConcurrently(t *testing.T) {
	service := testYtDlpHelperService(t)
	task := models.NewDownloadTask(models.DownloadRequest{URL: "https://example.test/video", OutputDir: t.TempDir()})
	// The helper is selected by the environment inherited by the child process.
	_ = os.Setenv("GO_WANT_YTDLP_HELPER", "1")
	defer os.Unsetenv("GO_WANT_YTDLP_HELPER")

	path, err := service.Download(context.Background(), task, nil)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if path != "" {
		t.Fatalf("Download() path = %q, want empty fallback for helper", path)
	}
}

func TestYtDlpDownloadCancellationReapsProcess(t *testing.T) {
	service := testYtDlpHelperService(t)
	task := models.NewDownloadTask(models.DownloadRequest{URL: "https://example.test/video", OutputDir: t.TempDir()})
	_ = os.Setenv("GO_WANT_YTDLP_HELPER", "1")
	_ = os.Setenv("GO_WANT_YTDLP_HELPER_BLOCK", "1")
	defer os.Unsetenv("GO_WANT_YTDLP_HELPER")
	defer os.Unsetenv("GO_WANT_YTDLP_HELPER_BLOCK")

	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		_, err := service.Download(context.Background(), task, func(models.ProgressInfo) {
			select {
			case <-started:
			default:
				close(started)
			}
		})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("helper process did not start")
	}
	task.Cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Download() cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Download() did not reap cancelled process")
	}
}

func TestFetchFormatsArgsUsesYTDLPDefaults(t *testing.T) {
	args := testYtDlpService().fetchFormatsArgs("https://example.test/video")

	if hasArgument(args, "--extractor-args") {
		t.Fatal("format extraction unexpectedly overrides yt-dlp extractor clients")
	}
	if got := args[len(args)-1]; got != "https://example.test/video" {
		t.Fatalf("URL = %q, want video URL", got)
	}
}

func TestBuildDownloadArgsAudioOnlyUsesSelectedFormat(t *testing.T) {
	service := testYtDlpService()
	req := models.DownloadRequest{
		Mode:          models.ModeAudioOnly,
		AudioFormat:   "mp3",
		AudioFormatID: "251",
		OutputDir:     t.TempDir(),
	}

	args := service.buildDownloadArgs(req)
	if got := formatArgument(args); got != "251" {
		t.Fatalf("audio format selector = %q, want %q", got, "251")
	}
}

func TestBuildDownloadArgsVideoOnlySelectsVideoFormats(t *testing.T) {
	service := testYtDlpService()
	req := models.DownloadRequest{
		Mode:      models.ModeVideoOnly,
		MaxHeight: 720,
		MaxFPS:    30,
		OutputDir: t.TempDir(),
	}

	args := service.buildDownloadArgs(req)
	got := formatArgument(args)
	want := "bv[height<=?720][fps<=?30]/bv/b[height<=?720][fps<=?30]/b"
	if got != want {
		t.Fatalf("video format selector = %q, want %q", got, want)
	}
}

func TestBuildDownloadArgsMultiAudioIncludesSelectedTracks(t *testing.T) {
	service := testYtDlpService()
	req := models.DownloadRequest{
		Mode:                  models.ModeVideoAudio,
		Container:             "mp4",
		VideoFormatID:         "137",
		SelectedAudioTrackIDs: []string{"140", "251"},
		OutputDir:             t.TempDir(),
	}

	args := service.buildDownloadArgs(req)
	got := formatArgument(args)
	want := "137+140+251/137+mergeall[vcodec=none]/137+ba/b"
	if got != want {
		t.Fatalf("multi-audio format selector = %q, want %q", got, want)
	}
	if !hasArgument(args, "--audio-multistreams") {
		t.Fatal("multi-audio download is missing --audio-multistreams")
	}
	if got := argumentValue(args, "--merge-output-format"); got != "mkv" {
		t.Fatalf("merge output format = %q, want %q", got, "mkv")
	}
}

func TestBuildDownloadArgsMultiAudioWithoutTracksUsesMergeAll(t *testing.T) {
	service := testYtDlpService()
	req := models.DownloadRequest{
		Mode:       models.ModeVideoAudio,
		Container:  "webm",
		MaxHeight:  1080,
		MultiAudio: true,
		OutputDir:  t.TempDir(),
	}

	args := service.buildDownloadArgs(req)
	got := formatArgument(args)
	want := "bv*[height<=?1080]+mergeall[vcodec=none]/b[height<=?1080]/b"
	if got != want {
		t.Fatalf("multi-audio format selector = %q, want %q", got, want)
	}
	if !hasArgument(args, "--audio-multistreams") {
		t.Fatal("multi-audio download is missing --audio-multistreams")
	}
	if got := argumentValue(args, "--merge-output-format"); got != "mkv" {
		t.Fatalf("merge output format = %q, want %q", got, "mkv")
	}
}

func TestBuildDownloadArgsAudioSelectionModes(t *testing.T) {
	tests := []struct {
		name             string
		selection        models.AudioSelectionMode
		tracks           []string
		wantFormat       string
		wantMultiStreams bool
	}{
		{
			name:       "none selects one best track",
			selection:  models.AudioSelectionNone,
			tracks:     []string{"140", "251"},
			wantFormat: "bv*+ba/b",
		},
		{
			name:             "all selects every available track",
			selection:        models.AudioSelectionAll,
			wantFormat:       "bv*+mergeall[vcodec=none]/b",
			wantMultiStreams: true,
		},
		{
			name:             "custom converts language codes",
			selection:        models.AudioSelectionCustom,
			tracks:           []string{"en", "251"},
			wantFormat:       "bv*+ba[language=en]+251/bv*+mergeall[vcodec=none]/bv*+ba/b",
			wantMultiStreams: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := models.DownloadRequest{
				Mode:                  models.ModeVideoAudio,
				Container:             "mkv",
				AudioSelectionMode:    tt.selection,
				SelectedAudioTrackIDs: tt.tracks,
				OutputDir:             t.TempDir(),
			}
			args := testYtDlpService().buildDownloadArgs(req)
			if got := formatArgument(args); got != tt.wantFormat {
				t.Fatalf("audio selection format = %q, want %q", got, tt.wantFormat)
			}
			if got := hasArgument(args, "--audio-multistreams"); got != tt.wantMultiStreams {
				t.Fatalf("audio multistreams = %t, want %t", got, tt.wantMultiStreams)
			}
			if hasArgument(args, "--extractor-args") {
				t.Fatal("audio selection unexpectedly overrides yt-dlp extractor clients")
			}
		})
	}
}

func TestBuildDownloadArgsSubtitleSelectionModes(t *testing.T) {
	tests := []struct {
		name        string
		selection   models.SubtitleSelectionMode
		write       bool
		langs       []string
		wantWrite   bool
		wantLangs   string
		wantSleep   bool
		wantRetry   bool
		wantClean   bool
		wantConvert bool
		wantIgnore  bool
	}{
		{
			name:      "none disables legacy subtitle switch",
			selection: models.SubtitleSelectionNone,
			write:     true,
		},
		{
			name:        "all writes every language",
			selection:   models.SubtitleSelectionAll,
			wantWrite:   true,
			wantLangs:   "all,-live_chat",
			wantSleep:   true,
			wantRetry:   true,
			wantClean:   true,
			wantConvert: true,
			wantIgnore:  true,
		},
		{
			name:        "custom writes selected languages",
			selection:   models.SubtitleSelectionCustom,
			langs:       []string{"en", "de"},
			wantWrite:   true,
			wantLangs:   "en,de",
			wantSleep:   true,
			wantRetry:   true,
			wantClean:   true,
			wantConvert: true,
			wantIgnore:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := models.DownloadRequest{
				Mode:                  models.ModeVideoAudio,
				Container:             "mkv",
				WriteSubtitles:        tt.write,
				SubtitleSelectionMode: tt.selection,
				SubtitleLangs:         tt.langs,
				OutputDir:             t.TempDir(),
			}
			args := testYtDlpService().buildDownloadArgs(req)
			if got := hasArgument(args, "--write-subs"); got != tt.wantWrite {
				t.Fatalf("write subtitles = %t, want %t", got, tt.wantWrite)
			}
			if got := argumentValue(args, "--sub-langs"); got != tt.wantLangs {
				t.Fatalf("subtitle languages = %q, want %q", got, tt.wantLangs)
			}
			if got := hasArgument(args, "--sleep-subtitles"); got != tt.wantSleep {
				t.Fatalf("subtitle sleep = %t, want %t", got, tt.wantSleep)
			}
			if tt.wantSleep && argumentValue(args, "--sleep-subtitles") != "5" {
				t.Fatalf("subtitle sleep duration = %q, want %q", argumentValue(args, "--sleep-subtitles"), "5")
			}
			if got := hasArgument(args, "--retry-sleep"); got != tt.wantRetry {
				t.Fatalf("subtitle retry backoff = %t, want %t", got, tt.wantRetry)
			}
			if tt.wantRetry && argumentValue(args, "--retry-sleep") != "http:exp=5:60" {
				t.Fatalf("subtitle retry backoff value = %q, want %q", argumentValue(args, "--retry-sleep"), "http:exp=5:60")
			}
			if got := hasArgument(args, "--compat-options"); got != tt.wantClean {
				t.Fatalf("subtitle cleanup option = %t, want %t", got, tt.wantClean)
			}
			if got := hasArgument(args, "--convert-subs"); got != tt.wantConvert {
				t.Fatalf("subtitle conversion = %t, want %t", got, tt.wantConvert)
			}
			if tt.wantConvert && argumentValue(args, "--convert-subs") != "srt" {
				t.Fatalf("subtitle conversion format = %q, want %q", argumentValue(args, "--convert-subs"), "srt")
			}
			if tt.wantClean && argumentValue(args, "--compat-options") != "no-keep-subs" {
				t.Fatalf("subtitle cleanup option value = %q, want %q", argumentValue(args, "--compat-options"), "no-keep-subs")
			}
			if got := hasArgument(args, "--ignore-errors"); got != tt.wantIgnore {
				t.Fatalf("subtitle error handling = %t, want %t", got, tt.wantIgnore)
			}
		})
	}
}

func TestBuildDownloadArgsMultipleSubtitlesUseVLCCompatibleContainer(t *testing.T) {
	for _, container := range []string{"mp4", "mkv", "webm"} {
		t.Run(container, func(t *testing.T) {
			req := models.DownloadRequest{
				Mode:                  models.ModeVideoAudio,
				Container:             container,
				SubtitleSelectionMode: models.SubtitleSelectionCustom,
				SubtitleLangs:         []string{"en", "de"},
				OutputDir:             t.TempDir(),
			}

			args := testYtDlpService().buildDownloadArgs(req)
			if got := argumentValue(args, "--sub-langs"); got != "en,de" {
				t.Fatalf("subtitle languages = %q, want %q", got, "en,de")
			}
			if !hasArgument(args, "--embed-subs") {
				t.Fatal("multiple subtitle selection is missing --embed-subs")
			}
			if got := argumentValue(args, "--compat-options"); got != "no-keep-subs" {
				t.Fatalf("subtitle cleanup option = %q, want %q", got, "no-keep-subs")
			}
			if got := argumentValue(args, "--convert-subs"); got != "srt" {
				t.Fatalf("subtitle conversion format = %q, want srt", got)
			}
			if got := argumentValue(args, "--merge-output-format"); got != "mkv" {
				t.Fatalf("merge output format = %q, want mkv for VLC subtitle rendering", got)
			}
			if got := argumentValue(args, "--remux-video"); got != "mkv" {
				t.Fatalf("remux output format = %q, want mkv for VLC subtitle rendering", got)
			}
		})
	}
}

func TestBuildDownloadArgsSubtitleEmbeddingPreservesMultiAudio(t *testing.T) {
	req := models.DownloadRequest{
		Mode:                  models.ModeVideoAudio,
		Container:             "mp4",
		MultiAudio:            true,
		SubtitleSelectionMode: models.SubtitleSelectionCustom,
		SubtitleLangs:         []string{"en", "hi"},
		OutputDir:             t.TempDir(),
	}

	args := testYtDlpService().buildDownloadArgs(req)
	if !hasArgument(args, "--audio-multistreams") {
		t.Fatal("subtitle embedding removed --audio-multistreams")
	}
	if got := argumentValue(args, "--sub-langs"); got != "en,hi" {
		t.Fatalf("subtitle languages = %q, want en,hi", got)
	}
	if got := argumentValue(args, "--merge-output-format"); got != "mkv" {
		t.Fatalf("merge output format = %q, want mkv", got)
	}
}

func TestBuildDownloadArgsVideoOnlySubtitleEmbeddingRemuxesToMkv(t *testing.T) {
	req := models.DownloadRequest{
		Mode:                  models.ModeVideoOnly,
		Container:             "mp4",
		SubtitleSelectionMode: models.SubtitleSelectionCustom,
		SubtitleLangs:         []string{"en", "hi"},
		OutputDir:             t.TempDir(),
	}

	args := testYtDlpService().buildDownloadArgs(req)
	if got := argumentValue(args, "--merge-output-format"); got != "mkv" {
		t.Fatalf("video-only merge output format = %q, want mkv", got)
	}
	if got := argumentValue(args, "--remux-video"); got != "mkv" {
		t.Fatalf("video-only remux output format = %q, want mkv", got)
	}
}

func TestDownloadArgsPrintFinalPathAfterPostProcessing(t *testing.T) {
	req := models.DownloadRequest{
		URL:                   "https://example.test/video",
		Mode:                  models.ModeVideoAudio,
		SubtitleSelectionMode: models.SubtitleSelectionCustom,
		SubtitleLangs:         []string{"en", "hi"},
		OutputDir:             t.TempDir(),
	}

	args := testYtDlpService().downloadArgs(req)
	if got := argumentValue(args, "--print"); got != "after_move:filepath" {
		t.Fatalf("post-processing path print = %q, want after_move:filepath", got)
	}
	if got := args[len(args)-1]; got != req.URL {
		t.Fatalf("download URL = %q, want %q", got, req.URL)
	}
}

func TestBaseArgsKeepsTLSCertificateVerificationEnabled(t *testing.T) {
	if hasArgument(testYtDlpService().baseArgs(), "--no-check-certificates") {
		t.Fatal("yt-dlp arguments disable TLS certificate verification")
	}
}

func TestBuildDownloadArgsEmptyCustomSubtitleSelectionDisablesSubtitleOptions(t *testing.T) {
	req := models.DownloadRequest{
		Mode:                  models.ModeVideoAudio,
		SubtitleSelectionMode: models.SubtitleSelectionCustom,
		OutputDir:             t.TempDir(),
	}

	args := testYtDlpService().buildDownloadArgs(req)
	for _, name := range []string{"--write-subs", "--write-auto-subs", "--embed-subs", "--convert-subs", "--compat-options", "--sleep-subtitles", "--retry-sleep", "--ignore-errors", "--sub-langs"} {
		if hasArgument(args, name) {
			t.Fatalf("empty custom subtitle selection unexpectedly includes %s", name)
		}
	}
}

func TestBuildDownloadArgsAudioOnlyDisablesSubtitleOptions(t *testing.T) {
	req := models.DownloadRequest{
		Mode:           models.ModeAudioOnly,
		WriteSubtitles: true,
		AudioFormat:    "mp3",
		OutputDir:      t.TempDir(),
	}

	args := testYtDlpService().buildDownloadArgs(req)
	for _, name := range []string{"--write-subs", "--write-auto-subs", "--embed-subs", "--convert-subs", "--compat-options", "--sleep-subtitles", "--retry-sleep", "--ignore-errors", "--sub-langs"} {
		if hasArgument(args, name) {
			t.Fatalf("audio-only download unexpectedly includes %s", name)
		}
	}
}

func TestBuildDownloadArgsPreferVP9UsesFormatSort(t *testing.T) {
	req := models.DownloadRequest{
		Mode:      models.ModeVideoAudio,
		Container: "mp4",
		PreferVP9: true,
		OutputDir: t.TempDir(),
	}

	args := testYtDlpService().buildDownloadArgs(req)
	if got := argumentValue(args, "--format-sort"); got != "vcodec:vp9,res,fps,acodec:opus,acodec:m4a" {
		t.Fatalf("format sort = %q, want VP9 preference", got)
	}
	if got := argumentValue(args, "--merge-output-format"); got != "mkv" {
		t.Fatalf("merge output format = %q, want %q", got, "mkv")
	}
}

func TestBuildDownloadArgsPreferVP9DoesNotOverrideExplicitVideo(t *testing.T) {
	req := models.DownloadRequest{
		Mode:          models.ModeVideoAudio,
		VideoFormatID: "137",
		AudioFormatID: "140",
		PreferVP9:     true,
		OutputDir:     t.TempDir(),
	}

	args := testYtDlpService().buildDownloadArgs(req)
	if got := formatArgument(args); got != "137+140" {
		t.Fatalf("explicit format selector = %q, want %q", got, "137+140")
	}
	if hasArgument(args, "--format-sort") {
		t.Fatal("explicit video format unexpectedly received VP9 format sort")
	}
}

func TestParseMediaKeepsBestAudioFormatPerLanguage(t *testing.T) {
	raw := map[string]any{
		"id":    "video-1",
		"title": "Example",
		"formats": []any{
			map[string]any{
				"format_id": "140", "acodec": "mp4a.40.2", "vcodec": "none",
				"language": "en", "abr": 128.0,
			},
			map[string]any{
				"format_id": "251", "acodec": "opus", "vcodec": "none",
				"language": "en", "abr": 160.0,
			},
			map[string]any{
				"format_id": "250", "acodec": "opus", "vcodec": "none",
				"language": "en", "abr": 70.0,
			},
			map[string]any{
				"format_id": "249", "acodec": "opus", "vcodec": "none",
				"abr": 50.0,
			},
		},
	}

	media := parseMedia(raw, "https://example.test/video")
	if len(media.AudioTracks) != 2 {
		t.Fatalf("audio tracks = %d, want one best track for each language", len(media.AudioTracks))
	}
	if media.AudioTracks[0].ID != "251" {
		t.Fatalf("English audio track = %q, want highest-quality Opus format 251", media.AudioTracks[0].ID)
	}
	if media.AudioTracks[1].ID != "249" {
		t.Fatalf("default audio track = %q, want format 249", media.AudioTracks[1].ID)
	}
}

func TestParseMediaKeepsAvailableSubtitleLanguageCodes(t *testing.T) {
	raw := map[string]any{
		"subtitles": map[string]any{
			" en ": []any{
				map[string]any{"ext": "vtt", "url": "https://example.test/en"},
			},
			"de": []any{
				map[string]any{"ext": "vtt", "url": "https://example.test/de"},
			},
			"empty": []any{},
			"":      []any{map[string]any{"ext": "vtt"}},
		},
		"automatic_captions": map[string]any{
			"hi": []any{
				map[string]any{"ext": "vtt", "url": "https://example.test/hi"},
			},
		},
	}

	media := parseMedia(raw, "https://example.test/video")
	if _, ok := media.Subtitles[" en "]; ok {
		t.Fatal("subtitle language code retained surrounding whitespace")
	}
	if len(media.Subtitles["en"]) != 1 || media.Subtitles["en"][0].Lang != "en" {
		t.Fatalf("parsed English subtitles = %#v, want one track retaining code en", media.Subtitles["en"])
	}
	if len(media.Subtitles["de"]) != 1 || media.Subtitles["de"][0].Lang != "de" {
		t.Fatalf("parsed German subtitles = %#v, want one track retaining code de", media.Subtitles["de"])
	}
	if len(media.AutomaticCaptions["hi"]) != 1 || media.AutomaticCaptions["hi"][0].Lang != "hi" {
		t.Fatalf("parsed Hindi captions = %#v, want one track retaining code hi", media.AutomaticCaptions["hi"])
	}
}

func testYtDlpService() *YtDlpService {
	return &YtDlpService{settings: func() config.AppSettings { return config.AppSettings{} }}
}

func formatArgument(args []string) string {
	return argumentValue(args, "-f")
}

func argumentValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func hasArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
