package services

import (
	"testing"

	"github.com/fazi-gondal/video-downloader-mygo/internal/config"
	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

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
