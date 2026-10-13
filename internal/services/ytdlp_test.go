package services

import (
	"testing"

	"github.com/fazi-gondal/video-downloader-mygo/internal/config"
	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

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
		name       string
		selection  models.SubtitleSelectionMode
		write      bool
		langs      []string
		wantWrite  bool
		wantLangs  string
		wantSleep  bool
		wantRetry  bool
		wantClean  bool
		wantIgnore bool
	}{
		{
			name:      "none disables legacy subtitle switch",
			selection: models.SubtitleSelectionNone,
			write:     true,
		},
		{
			name:       "all writes every language",
			selection:  models.SubtitleSelectionAll,
			wantWrite:  true,
			wantLangs:  "all,-live_chat",
			wantSleep:  true,
			wantRetry:  true,
			wantClean:  true,
			wantIgnore: true,
		},
		{
			name:       "custom writes selected languages",
			selection:  models.SubtitleSelectionCustom,
			langs:      []string{"en", "de"},
			wantWrite:  true,
			wantLangs:  "en,de",
			wantSleep:  true,
			wantRetry:  true,
			wantClean:  true,
			wantIgnore: true,
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
			if tt.wantClean && argumentValue(args, "--compat-options") != "no-keep-subs" {
				t.Fatalf("subtitle cleanup option value = %q, want %q", argumentValue(args, "--compat-options"), "no-keep-subs")
			}
			if got := hasArgument(args, "--ignore-errors"); got != tt.wantIgnore {
				t.Fatalf("subtitle error handling = %t, want %t", got, tt.wantIgnore)
			}
		})
	}
}

func TestBuildDownloadArgsMultipleSubtitlesKeepContainerAndCleanSidecars(t *testing.T) {
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
			if got := argumentValue(args, "--merge-output-format"); got != container {
				t.Fatalf("merge output format = %q, want selected container %q", got, container)
			}
		})
	}
}

func TestBuildDownloadArgsEmptyCustomSubtitleSelectionDisablesSubtitleOptions(t *testing.T) {
	req := models.DownloadRequest{
		Mode:                  models.ModeVideoAudio,
		SubtitleSelectionMode: models.SubtitleSelectionCustom,
		OutputDir:             t.TempDir(),
	}

	args := testYtDlpService().buildDownloadArgs(req)
	for _, name := range []string{"--write-subs", "--write-auto-subs", "--embed-subs", "--compat-options", "--sleep-subtitles", "--retry-sleep", "--ignore-errors", "--sub-langs"} {
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
	for _, name := range []string{"--write-subs", "--write-auto-subs", "--embed-subs", "--compat-options", "--sleep-subtitles", "--retry-sleep", "--ignore-errors", "--sub-langs"} {
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
