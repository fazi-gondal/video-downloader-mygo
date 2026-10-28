package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/config"
	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
	"github.com/fazi-gondal/video-downloader-mygo/internal/tools"
)

func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_YTDLP_HELPER") == "1" {
		if os.Getenv("GO_WANT_YTDLP_HELPER_BLOCK") == "1" {
			fmt.Fprintln(os.Stdout, "PROGRESS|downloading|0|100|NA|NA|NA|0|1")
			writeYtDlpHelperPartial()
			for {
				fmt.Fprintln(os.Stderr, strings.Repeat("diagnostic ", 512))
				time.Sleep(10 * time.Millisecond)
			}
		}
		fmt.Fprintln(os.Stdout, "PROGRESS|downloading|40|100|NA|20|3.5|1|1")
		fmt.Fprintln(os.Stdout, "PROGRESS|finished|100|100|NA|NA|NA|1|1")
		fmt.Fprintln(os.Stderr, strings.Repeat("diagnostic ", 512))
		writeYtDlpHelperOutput()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func writeYtDlpHelperPartial() {
	var outputTemplate string
	for i := 0; i+1 < len(os.Args); i++ {
		if os.Args[i] == "-o" {
			outputTemplate = os.Args[i+1]
			break
		}
	}
	if outputTemplate == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(outputTemplate), 0o755)
	_ = os.WriteFile(filepath.Join(filepath.Dir(outputTemplate), "partial-video.mp4.part"), []byte("partial"), 0o644)
}

func writeYtDlpHelperOutput() {
	if os.Getenv("GO_WANT_YTDLP_HELPER_OUTPUT") != "1" {
		return
	}
	var outputTemplate string
	for i := 0; i+1 < len(os.Args); i++ {
		if os.Args[i] == "-o" {
			outputTemplate = os.Args[i+1]
			break
		}
	}
	if outputTemplate == "" {
		return
	}
	name := "helper-output"
	if url := os.Args[len(os.Args)-1]; url != "" {
		name = strings.TrimSuffix(url, "/")
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if name == "" {
			name = "helper-output"
		}
	}
	path := filepath.Join(filepath.Dir(outputTemplate), "helper-"+name+".mp4")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(name), 0o644)
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

func TestYtDlpDownloadDoesNotUseUnrelatedOutputFile(t *testing.T) {
	service := testYtDlpHelperService(t)
	outputDir := t.TempDir()
	unrelated := filepath.Join(outputDir, "unrelated.mp4")
	if err := os.WriteFile(unrelated, []byte("not this download"), 0o644); err != nil {
		t.Fatal(err)
	}
	task := models.NewDownloadTask(models.DownloadRequest{URL: "https://example.test/video", OutputDir: outputDir})
	_ = os.Setenv("GO_WANT_YTDLP_HELPER", "1")
	defer os.Unsetenv("GO_WANT_YTDLP_HELPER")

	path, err := service.Download(context.Background(), task, nil)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if path != "" {
		t.Fatalf("Download() path = %q, want no output from this request", path)
	}
}

func TestYtDlpDownloadAssociatesConcurrentOutputs(t *testing.T) {
	service := testYtDlpHelperService(t)
	outputDir := t.TempDir()
	_ = os.Setenv("GO_WANT_YTDLP_HELPER", "1")
	_ = os.Setenv("GO_WANT_YTDLP_HELPER_OUTPUT", "1")
	defer os.Unsetenv("GO_WANT_YTDLP_HELPER")
	defer os.Unsetenv("GO_WANT_YTDLP_HELPER_OUTPUT")

	tasks := []*models.DownloadTask{
		models.NewDownloadTask(models.DownloadRequest{URL: "https://example.test/one", OutputDir: outputDir}),
		models.NewDownloadTask(models.DownloadRequest{URL: "https://example.test/two", OutputDir: outputDir}),
	}
	start := make(chan struct{})
	paths := make([]string, len(tasks))
	errs := make([]error, len(tasks))
	var wg sync.WaitGroup
	for i, task := range tasks {
		wg.Add(1)
		go func(i int, task *models.DownloadTask) {
			defer wg.Done()
			<-start
			paths[i], errs[i] = service.Download(context.Background(), task, nil)
		}(i, task)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("download %d error = %v", i, err)
		}
	}
	wantPaths := map[string]string{
		"helper-one.mp4": "one",
		"helper-two.mp4": "two",
	}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		name := filepath.Base(path)
		want, ok := wantPaths[name]
		if !ok {
			t.Fatalf("Download() path = %q, want one of the task-specific outputs", path)
		}
		if seen[name] {
			t.Fatalf("Download() associated %q with more than one task", name)
		}
		seen[name] = true
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %q: %v", path, err)
		}
		if string(contents) != want {
			t.Fatalf("contents of %q = %q, want %q", path, contents, want)
		}
	}
}

func TestYtDlpDownloadReportsProgressFromLiteralMarker(t *testing.T) {
	service := testYtDlpHelperService(t)
	task := models.NewDownloadTask(models.DownloadRequest{URL: "https://example.test/video", OutputDir: t.TempDir()})
	_ = os.Setenv("GO_WANT_YTDLP_HELPER", "1")
	defer os.Unsetenv("GO_WANT_YTDLP_HELPER")

	var progress []models.ProgressInfo
	if _, err := service.Download(context.Background(), task, func(p models.ProgressInfo) {
		progress = append(progress, p)
	}); err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if len(progress) != 2 {
		t.Fatalf("progress callbacks = %d, want 2", len(progress))
	}
	if got := progress[0]; got.Status != "downloading" || got.DownloadedBytes != 40 || got.TotalBytes != 100 || got.SpeedBps != 20 || got.ETA != 3500*time.Millisecond {
		t.Fatalf("downloading progress = %+v, want parsed bytes/total/speed/ETA", got)
	}
	if got := progress[1]; got.Status != "finished" || got.Percent != 100 {
		t.Fatalf("finished progress = %+v, want finished at 100%%", got)
	}
}

func TestParseProgressLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want models.ProgressInfo
		ok   bool
	}{
		{
			name: "status and exact total",
			line: "PROGRESS|downloading|512|2048|NA|1024.5|1.25|2|8",
			want: models.ProgressInfo{
				Status: "downloading", DownloadedBytes: 512, TotalBytes: 2048,
				SpeedBps: 1024.5, ETA: 1250 * time.Millisecond, FragmentIndex: 2,
				FragmentCount: 8, Percent: 25,
			},
			ok: true,
		},
		{
			name: "estimate and unavailable values",
			line: "PROGRESS|downloading|10|NA|100|NA|NA|0|0",
			want: models.ProgressInfo{
				Status: "downloading", DownloadedBytes: 10, TotalBytes: 100,
				TotalIsEstimate: true, Percent: 10,
			},
			ok: true,
		},
		{
			name: "hls decimal estimate and float speed",
			line: "PROGRESS|downloading|15624624|NA|72900628.8|4223233.75|14.71|49|228",
			want: models.ProgressInfo{
				Status:          "downloading",
				DownloadedBytes: 15624624,
				TotalBytes:      72900629,
				TotalIsEstimate: true,
				SpeedBps:        4223233.75,
				ETA:             14710 * time.Millisecond,
				FragmentIndex:   49,
				FragmentCount:   228,
				Percent:         float64(15624624) / 72900629 * 100,
			},
			ok: true,
		},
		{name: "unmarked output", line: "download:10|100|100|NA|NA|0|1", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseProgressLine(tt.line)
			if ok != tt.ok {
				t.Fatalf("parseProgressLine ok = %t, want %t", ok, tt.ok)
			}
			if !tt.ok {
				return
			}
			if got != tt.want {
				t.Fatalf("parseProgressLine = %+v, want %+v", got, tt.want)
			}
		})
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

func TestYtDlpDownloadPausePreservesPartialAndResumeReusesStaging(t *testing.T) {
	service := testYtDlpHelperService(t)
	outputDir := t.TempDir()
	task := models.NewDownloadTask(models.DownloadRequest{URL: "https://example.test/video", OutputDir: outputDir})
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
	deadline := time.Now().Add(time.Second)
	for task.StagingDir() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stagingDir := task.StagingDir()
	if stagingDir == "" {
		t.Fatal("download did not record a staging directory")
	}
	task.RequestPause()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("paused Download() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("paused Download() did not stop")
	}
	if _, err := os.Stat(stagingDir); err != nil {
		t.Fatalf("staging directory after pause: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stagingDir, "partial-video.mp4.part")); err != nil {
		t.Fatalf("partial file after pause: %v", err)
	}

	task.ResetForResume()
	_ = os.Unsetenv("GO_WANT_YTDLP_HELPER_BLOCK")
	_ = os.Setenv("GO_WANT_YTDLP_HELPER_OUTPUT", "1")
	defer os.Unsetenv("GO_WANT_YTDLP_HELPER_OUTPUT")
	path, err := service.Download(context.Background(), task, nil)
	if err != nil {
		t.Fatalf("resumed Download() error = %v", err)
	}
	if path == "" {
		t.Fatal("resumed Download() returned an empty output path")
	}
	if _, err := os.Stat(stagingDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging directory after successful resume = %v, want removed", err)
	}
}

func TestFetchFormatsArgsUsesCookieFreeYouTubeClient(t *testing.T) {
	args := testYtDlpService().fetchFormatsArgs("https://example.test/video")

	if got := argumentValue(args, "--extractor-args"); got != "youtube:player_client=default,web_embedded,-android_vr" {
		t.Fatalf("YouTube extractor args = %q, want embedded fallback without cookies", got)
	}
	if hasArgument(args, "--cookies-from-browser") {
		t.Fatal("default format extraction unexpectedly requests browser cookies")
	}
	if !hasArgument(args, "--no-js-runtimes") {
		t.Fatal("default format extraction unexpectedly depends on an external JavaScript runtime")
	}
	if got := args[len(args)-1]; got != "https://example.test/video" {
		t.Fatalf("URL = %q, want video URL", got)
	}
}

func TestBaseArgsDoesNotRequestCookiesByDefault(t *testing.T) {
	args := testYtDlpService().baseArgs()
	if hasArgument(args, "--cookies-from-browser") {
		t.Fatal("default yt-dlp arguments unexpectedly request browser cookies")
	}
}

func TestSocialPlatformRecognizesSupportedHostsOnly(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "TikTok", url: "https://www.tiktok.com/@creator/video/123", want: "TikTok"},
		{name: "TikTok short host", url: "https://vm.tiktok.com/ZM123/", want: "TikTok"},
		{name: "Instagram", url: "https://www.instagram.com/reel/abc/", want: "Instagram"},
		{name: "X", url: "https://x.com/user/status/123", want: "X"},
		{name: "Twitter alias", url: "https://mobile.twitter.com/user/status/123", want: "X"},
		{name: "untrusted suffix", url: "https://notinstagram.com/reel/abc", want: ""},
		{name: "invalid scheme", url: "file:///tmp/video.mp4", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SocialPlatform(tt.url); got != tt.want {
				t.Fatalf("SocialPlatform(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestYouTubeShortURLRecognition(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "canonical", url: "https://www.youtube.com/shorts/abc123", want: true},
		{name: "mobile", url: "https://m.youtube.com/shorts/abc123?feature=share", want: true},
		{name: "watch video", url: "https://www.youtube.com/watch?v=abc123", want: false},
		{name: "shorts without id", url: "https://www.youtube.com/shorts/", want: false},
		{name: "lookalike host", url: "https://notyoutube.com/shorts/abc123", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsYouTubeShortURL(tt.url); got != tt.want {
				t.Fatalf("IsYouTubeShortURL(%q) = %t, want %t", tt.url, got, tt.want)
			}
		})
	}
	if got := DirectDownloadPlatform("https://www.youtube.com/shorts/abc123"); got != "YouTube Shorts" {
		t.Fatalf("DirectDownloadPlatform(YouTube Shorts) = %q, want YouTube Shorts", got)
	}
	if IsOneClickDownloadURL("https://www.youtube.com/watch?v=abc123") {
		t.Fatal("regular YouTube video unexpectedly uses the Shorts one-click flow")
	}
}

func TestYouTubeShortAnalyzeUsesSingleVideoCookieFreeArgs(t *testing.T) {
	service := &YtDlpService{settings: func() config.AppSettings {
		return config.AppSettings{CookiesBrowser: "chrome"}
	}}
	args := service.analyzeArgs("https://www.youtube.com/shorts/abc123")
	if !hasArgument(args, "--no-playlist") {
		t.Fatal("YouTube Shorts metadata analysis must remain a single-video extraction")
	}
	if hasArgument(args, "--cookies-from-browser") {
		t.Fatal("YouTube Shorts metadata analysis unexpectedly requests browser cookies")
	}
}

func TestOneClickURLUsesRegularBestSelectorForYouTubeShorts(t *testing.T) {
	req := models.DownloadRequest{
		URL:       "https://www.youtube.com/shorts/abc123",
		Mode:      models.ModeVideoAudio,
		Container: "mp4",
	}
	got := videoAudioSelector(req)
	if strings.Contains(got, "watermarked") {
		t.Fatalf("YouTube Shorts selector unexpectedly uses social watermark filter: %q", got)
	}
	if got != "bv*[ext=mp4]+ba[ext=m4a]/bv*+ba/b" {
		t.Fatalf("YouTube Shorts selector = %q, want best MP4 selector", got)
	}
}

func TestDownloadOutputTemplatesDoNotContainVideoIDs(t *testing.T) {
	service := testYtDlpService()
	tests := []struct {
		name          string
		req           models.DownloadRequest
		wantSubstring string
	}{
		{
			name:          "single video",
			req:           models.DownloadRequest{URL: "https://www.youtube.com/watch?v=abc", Mode: models.ModeVideoAudio, Container: "mp4", OutputDir: t.TempDir()},
			wantSubstring: "%(title).200B.%(ext)s",
		},
		{
			name:          "playlist entry",
			req:           models.DownloadRequest{URL: "https://www.youtube.com/watch?v=abc", Mode: models.ModeVideoAudio, Container: "mp4", PlaylistTitle: "My Playlist", PlaylistIndex: 1, OutputDir: t.TempDir()},
			wantSubstring: filepath.Join("My Playlist", "01 - %(title).150B.%(ext)s"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := service.buildDownloadArgs(tt.req)
			output := argumentValue(args, "-o")
			if !strings.Contains(output, tt.wantSubstring) {
				t.Fatalf("output template = %q, want %q", output, tt.wantSubstring)
			}
			if strings.Contains(output, "%(id)") {
				t.Fatalf("output template still contains a video ID placeholder: %q", output)
			}
			if tt.req.PlaylistTitle != "" && !hasArgument(args, "--no-playlist") {
				t.Fatal("playlist entry download is missing --no-playlist")
			}
		})
	}
}

func TestPlaylistOutputUsesSanitizedTitleFolderAndIndex(t *testing.T) {
	req := models.DownloadRequest{
		URL:           "https://www.youtube.com/watch?v=abc",
		Mode:          models.ModeVideoAudio,
		Container:     "mp4",
		PlaylistTitle: `My: Playlist/2026`,
		PlaylistIndex: 7,
		OutputDir:     t.TempDir(),
	}
	output := argumentValue(testYtDlpService().buildDownloadArgs(req), "-o")
	want := filepath.Join(req.OutputDir, "My_ Playlist_2026", "07 - %(title).150B.%(ext)s")
	if output != want {
		t.Fatalf("playlist output template = %q, want %q", output, want)
	}
}

func TestPromoteStagedDownloadAvoidsExistingTitleCollision(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	output := filepath.Join(root, "output")
	if err := os.MkdirAll(staging, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(output, 0o750); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(output, "Same title.mp4")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(staging, "Same title.mp4")
	if err := os.WriteFile(staged, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := promoteStagedDownload(staging, output, staged)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(output, "Same title (2).mp4")
	if got != want {
		t.Fatalf("promoted path = %q, want %q", got, want)
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "old" {
		t.Fatalf("existing output was changed: data=%q err=%v", data, err)
	}
	if data, err := os.ReadFile(want); err != nil || string(data) != "new" {
		t.Fatalf("new output = %q err=%v, want new content", data, err)
	}
}

func TestSocialDownloadUsesBestVideoAudioWithoutCookies(t *testing.T) {
	service := &YtDlpService{settings: func() config.AppSettings {
		return config.AppSettings{CookiesBrowser: "chrome"}
	}}
	req := models.DownloadRequest{
		URL:       "https://www.tiktok.com/@creator/video/123",
		Mode:      models.ModeVideoAudio,
		Container: "mp4",
		NoCookies: true,
		OutputDir: t.TempDir(),
	}

	args := service.buildDownloadArgs(req)
	if got := formatArgument(args); got != "bv*[format_note!*=?watermarked]+ba/b[format_note!*=?watermarked]/bv*+ba/b" {
		t.Fatalf("social format selector = %q, want best video/audio selector", got)
	}
	if hasArgument(args, "--cookies-from-browser") {
		t.Fatal("social download unexpectedly requests browser cookies")
	}
	if !hasArgument(args, "--no-playlist") {
		t.Fatal("social download must remain a single-video download")
	}
	if got := argumentValue(args, "--merge-output-format"); got != "mp4" {
		t.Fatalf("social merge format = %q, want mp4", got)
	}
}

func TestSocialFormatExtractionIgnoresConfiguredCookies(t *testing.T) {
	service := &YtDlpService{settings: func() config.AppSettings {
		return config.AppSettings{
			CookiesBrowser: "chrome",
			CustomHeaders:  map[string]string{"Cookie": "session=secret", "X-Test": "ok"},
		}
	}}
	args := service.fetchFormatsArgs("https://www.instagram.com/reel/abc/")
	if hasArgument(args, "--cookies-from-browser") {
		t.Fatal("social metadata extraction unexpectedly requests browser cookies")
	}
	if hasArgument(args, "Cookie:session=secret") {
		t.Fatal("social metadata extraction unexpectedly forwards a Cookie header")
	}
	if !hasArgument(args, "X-Test:ok") {
		t.Fatal("non-cookie custom headers should remain available for social extraction")
	}
}

func TestSocialAnalyzeFetchesSingleVideoMetadataWithoutCookies(t *testing.T) {
	service := &YtDlpService{settings: func() config.AppSettings {
		return config.AppSettings{CookiesBrowser: "chrome"}
	}}
	args := service.analyzeArgs("https://x.com/creator/status/123")
	if !hasArgument(args, "--no-playlist") {
		t.Fatal("social metadata analysis must remain a single-video extraction")
	}
	if hasArgument(args, "--cookies-from-browser") {
		t.Fatal("social metadata analysis unexpectedly requests browser cookies")
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

func TestBuildDownloadArgsPreservesOneSidedExplicitFormatSelection(t *testing.T) {
	tests := []struct {
		name      string
		videoID   string
		audioID   string
		wantParts []string
	}{
		{name: "video only", videoID: "137", wantParts: []string{"137+ba/b"}},
		{name: "audio only", audioID: "140", wantParts: []string{"bv*+140/b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := models.DownloadRequest{
				Mode: models.ModeVideoAudio, Container: "mp4",
				VideoFormatID: tt.videoID, AudioFormatID: tt.audioID,
				OutputDir: t.TempDir(),
			}
			got := formatArgument(testYtDlpService().buildDownloadArgs(req))
			for _, want := range tt.wantParts {
				if !strings.Contains(got, want) {
					t.Fatalf("format selector = %q, want it to contain %q", got, want)
				}
			}
		})
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
			if got := argumentValue(args, "--extractor-args"); got != "youtube:player_client=default,web_embedded,-android_vr" {
				t.Fatalf("YouTube extractor args = %q, want embedded fallback", got)
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
			if hasArgument(args, "--write-auto-subs") {
				t.Fatal("automatic captions must not be requested")
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

func TestDownloadArgsProgressTemplateContainsLiteralMarker(t *testing.T) {
	args := testYtDlpService().downloadArgs(models.DownloadRequest{URL: "https://example.test/video"})
	template := argumentValue(args, "--progress-template")
	if !strings.HasPrefix(template, "download:PROGRESS|") {
		t.Fatalf("progress template = %q, want literal PROGRESS marker after selector", template)
	}
	if !strings.Contains(template, "%(progress.status)s") {
		t.Fatalf("progress template = %q, want progress status", template)
	}
}

func TestDownloadArgsExplicitlyResumePartialDownloads(t *testing.T) {
	args := testYtDlpService().downloadArgs(models.DownloadRequest{URL: "https://example.test/video"})
	if !hasArgument(args, "--continue") || !hasArgument(args, "--part") {
		t.Fatalf("download args = %#v, want --continue and --part", args)
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
				"format_id": "140", "format_note": "low", "acodec": "mp4a.40.2", "vcodec": "none",
				"language": "en", "abr": 128.0,
			},
			map[string]any{
				"format_id": "251", "format_note": "high", "acodec": "opus", "vcodec": "none",
				"language": "en", "abr": 160.0,
			},
			map[string]any{
				"format_id": "250", "format_note": "medium", "acodec": "opus", "vcodec": "none",
				"language": "en", "abr": 70.0,
			},
			map[string]any{
				"format_id": "249", "acodec": "opus", "vcodec": "none",
				"abr": 50.0,
			},
			map[string]any{
				"format_id": "250", "format_note": "medium", "acodec": "opus", "vcodec": "none",
				"abr": 70.0,
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
	if media.AudioTracks[1].ID != "250" {
		t.Fatalf("default audio track = %q, want highest-quality format 250", media.AudioTracks[1].ID)
	}
}

func TestParseMediaUsesAudioTBRWhenABRIsMissing(t *testing.T) {
	raw := map[string]any{
		"id": "video-1",
		"formats": []any{
			map[string]any{
				"format_id": "audio", "acodec": "opus", "vcodec": "none",
				"language": "en", "tbr": 160.0,
			},
		},
	}
	media := parseMedia(raw, "https://example.test/video")
	if len(media.AudioTracks) != 1 || media.AudioTracks[0].Bitrate != 160 {
		t.Fatalf("audio track bitrate = %#v, want 160 kbps from tbr", media.AudioTracks)
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

func TestParseMediaDetectsYouTubeMeshVideoAs360(t *testing.T) {
	raw := map[string]any{
		"formats": []any{
			map[string]any{
				"format_id":   "248",
				"format_note": "1080s, mesh",
				"vcodec":      "vp9",
			},
		},
	}

	media := parseMedia(raw, "https://www.youtube.com/watch?v=example")
	if !media.Is360 {
		t.Fatal("mesh format was not detected as spherical video")
	}
}

func TestVideoSelectorPrefersEquirectangularHLSFor360Video(t *testing.T) {
	req := models.DownloadRequest{
		Mode:      models.ModeVideoAudio,
		Container: "mkv",
		MaxHeight: 1080,
		Is360:     true,
	}

	got := videoAudioSelector(req)
	want := "bv*[protocol=m3u8_native][height<=?1080]+ba/bv*[protocol=m3u8_native][height<=?1080]/b[height<=?1080]/b"
	if got != want {
		t.Fatalf("videoAudioSelector(360) = %q, want %q", got, want)
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

func TestAryPlusExtraction(t *testing.T) {
	if strings.TrimSpace(os.Getenv("ARYPLUS_API_KEY")) == "" {
		t.Skip("ARYPLUS_API_KEY is not configured")
	}
	yt := tools.YtDlp()
	if yt == "" {
		t.Skip("yt-dlp binary not found")
	}

	service := NewYtDlpService("", func() config.AppSettings { return config.AppSettings{} })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	url := "https://aryplus.tv/video/v2/3/6a6e0b1e9bc4b079859bc14b/6a57868b5bf57c474cc00a50"
	result, err := service.Analyze(ctx, url)
	if err != nil {
		t.Fatalf("Analyze ARY Plus URL error: %v", err)
	}

	media, ok := result.(*models.MediaInfo)
	if !ok || media == nil {
		t.Fatalf("expected *models.MediaInfo, got %T", result)
	}
	if !strings.Contains(media.Title, "Dar-E-Nijaat") {
		t.Errorf("expected title to contain 'Dar-E-Nijaat', got %q", media.Title)
	}
	if media.Thumbnail == "" {
		t.Errorf("expected non-empty thumbnail, got %q", media.Thumbnail)
	}

	formats, err := service.FetchFormats(ctx, url)
	if err != nil {
		t.Fatalf("FetchFormats error: %v", err)
	}
	if len(formats.Formats) == 0 {
		t.Fatalf("expected at least one format variant")
	}

	seriesURL := "https://aryplus.tv/series/6a57868b5bf57c474cc00a50"
	seriesResult, err := service.Analyze(ctx, seriesURL)
	if err != nil {
		t.Fatalf("Analyze series error: %v", err)
	}
	playlist, ok := seriesResult.(*models.PlaylistInfo)
	if !ok || playlist == nil {
		t.Fatalf("expected *models.PlaylistInfo, got %T", seriesResult)
	}
	if len(playlist.Entries) == 0 {
		t.Fatalf("expected playlist entries, got none")
	}
	if !strings.Contains(playlist.Entries[0].Title, "Dar-E-Nijaat") {
		t.Errorf("expected entry title to contain 'Dar-E-Nijaat', got %q", playlist.Entries[0].Title)
	}
}

func TestVideoAudioSelectorMuxedFallbacks(t *testing.T) {
	// When explicit format ID is chosen (e.g. 860 for 720p on ARY Plus HLS):
	reqExplicit := models.DownloadRequest{
		VideoFormatID: "860",
		Mode:          models.ModeVideoAudio,
	}
	gotExplicit := videoAudioSelector(reqExplicit)
	wantExplicit := "860+ba/b/860/b"
	if gotExplicit != wantExplicit {
		t.Errorf("videoAudioSelector(explicit) = %q, want %q", gotExplicit, wantExplicit)
	}

	// When resolution cap is chosen (e.g. 720p with default VP9 preference):
	reqHeight := models.DownloadRequest{
		MaxHeight: 720,
		PreferVP9: true,
		Mode:      models.ModeVideoAudio,
	}
	gotHeight := videoAudioSelector(reqHeight)
	wantHeight := "bv*[height<=?720]+ba/bv*[height<=?720]/b[height<=?720]/b"
	if gotHeight != wantHeight {
		t.Errorf("videoAudioSelector(height) = %q, want %q", gotHeight, wantHeight)
	}

	// When MP4 container is chosen with height cap:
	reqMP4 := models.DownloadRequest{
		MaxHeight: 480,
		Container: "mp4",
		PreferVP9: false,
		Mode:      models.ModeVideoAudio,
	}
	gotMP4 := videoAudioSelector(reqMP4)
	wantMP4 := "bv*[ext=mp4][height<=?480]+ba[ext=m4a]/bv*[height<=?480]+ba/b[height<=?480]/b"
	if gotMP4 != wantMP4 {
		t.Errorf("videoAudioSelector(mp4 height) = %q, want %q", gotMP4, wantMP4)
	}
}
