package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

func TestFFmpegHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_FFMPEG_HELPER") != "1" {
		return
	}
	output := os.Args[len(os.Args)-1]
	if err := os.WriteFile(output, []byte("converted"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stdout, "out_time_ms=1000")
	os.Exit(0)
}

func TestFFprobeDurationHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_FFPROBE_DURATION_HELPER") != "1" {
		return
	}
	time.Sleep(8 * time.Second)
}

func TestProbeDurationTimesOut(t *testing.T) {
	service := &FFmpegService{ffprobe: "fake-ffprobe"}
	service.command = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFFprobeDurationHelperProcess$")
		cmd.Env = append(os.Environ(), "GO_WANT_FFPROBE_DURATION_HELPER=1")
		return cmd
	}

	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	started := time.Now()
	if got := service.probeDuration(ctx, "source.mp4"); got != 0 {
		t.Fatalf("probeDuration() = %v, want zero after probe timeout", got)
	}
	elapsed := time.Since(started)
	if elapsed < ffprobeDurationTimeout-100*time.Millisecond {
		t.Fatalf("probeDuration() returned after %v, want it to wait for its timeout of %v", elapsed, ffprobeDurationTimeout)
	}
	if elapsed > 6*time.Second {
		t.Fatalf("probeDuration() took %v, want it to stop near its %v timeout", elapsed, ffprobeDurationTimeout)
	}
}

func TestConvertCancellationPreservesOriginalFile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "input.mkv")
	if err := os.WriteFile(source, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	service := &FFmpegService{ffmpeg: "fake-ffmpeg", remuxCache: make(map[string]remuxCacheEntry)}
	service.command = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		helperArgs := []string{"-test.run=TestFFmpegHelperProcess", "--"}
		helperArgs = append(helperArgs, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], helperArgs...)
		cmd.Env = append(os.Environ(), "GO_WANT_FFMPEG_HELPER=1")
		return cmd
	}
	job := models.NewConversionJob(models.ConversionRequest{
		Source:       source,
		TargetFormat: "mp4",
		Kind:         models.KindVideo,
		KeepOriginal: false,
	})

	_, err := service.Convert(context.Background(), job, func(percent float64) {
		if percent == 100 {
			job.Cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Convert() error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("original file stat after cancellation: %v", err)
	}
}

func TestConvertSuccessfulCleanupHonorsKeepOriginalSetting(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "input.mkv")
	if err := os.WriteFile(source, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	service := &FFmpegService{ffmpeg: "fake-ffmpeg", remuxCache: make(map[string]remuxCacheEntry)}
	service.command = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		helperArgs := []string{"-test.run=TestFFmpegHelperProcess", "--"}
		helperArgs = append(helperArgs, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], helperArgs...)
		cmd.Env = append(os.Environ(), "GO_WANT_FFMPEG_HELPER=1")
		return cmd
	}
	job := models.NewConversionJob(models.ConversionRequest{
		Source:       source,
		TargetFormat: "mp4",
		Kind:         models.KindVideo,
		KeepOriginal: false,
	})

	output, err := service.Convert(context.Background(), job, nil)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if output == "" {
		t.Fatal("Convert() returned an empty output path")
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original file stat after successful cleanup = %v, want not exist", err)
	}
}

func TestConversionArgsAudioCodecAndBitrate(t *testing.T) {
	tests := []struct {
		name        string
		format      string
		codec       string
		wantBitrate bool
	}{
		{name: "mp3", format: "mp3", codec: "libmp3lame", wantBitrate: true},
		{name: "m4a", format: "m4a", codec: "aac", wantBitrate: true},
		{name: "aac", format: "aac", codec: "aac", wantBitrate: true},
		{name: "opus", format: "opus", codec: "libopus", wantBitrate: true},
		{name: "flac is lossless", format: "flac", codec: "flac", wantBitrate: false},
		{name: "wav is lossless", format: "wav", codec: "pcm_s16le", wantBitrate: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := models.ConversionRequest{
				Source:           "source.mp4",
				TargetFormat:     tt.format,
				Kind:             models.KindAudio,
				AudioBitrateKbps: 320,
			}
			args, err := conversionArgs(req, "output."+tt.format)
			if err != nil {
				t.Fatalf("conversionArgs() error = %v", err)
			}
			if !containsPair(args, "-c:a", tt.codec) {
				t.Fatalf("conversionArgs() = %v, want codec %q", args, tt.codec)
			}
			gotBitrate := containsPair(args, "-b:a", "320k")
			if gotBitrate != tt.wantBitrate {
				t.Fatalf("conversionArgs() = %v, bitrate present = %t, want %t", args, gotBitrate, tt.wantBitrate)
			}
			if !containsPair(args, "-vn", "") {
				t.Fatalf("conversionArgs() = %v, want -vn", args)
			}
		})
	}
}

func TestConversionArgsVideoCodecsMatchContainer(t *testing.T) {
	tests := []struct {
		name      string
		format    string
		wantArgs  []string
		forbidden []string
	}{
		{
			name:      "webm",
			format:    "webm",
			wantArgs:  []string{"-c:v", "libvpx-vp9", "-c:a", "libopus"},
			forbidden: []string{"libx264", "aac"},
		},
		{
			name:      "avi",
			format:    "avi",
			wantArgs:  []string{"-c:v", "mpeg4", "-c:a", "libmp3lame"},
			forbidden: []string{"libx264", "aac"},
		},
		{
			name:     "mp4",
			format:   "mp4",
			wantArgs: []string{"-c:v", "libx264", "-c:a", "aac", "-b:a", "192k"},
		},
		{
			name:     "mkv",
			format:   "mkv",
			wantArgs: []string{"-c:v", "libx264", "-c:a", "aac", "-b:a", "192k"},
		},
		{
			name:     "mov",
			format:   "mov",
			wantArgs: []string{"-c:v", "libx264", "-c:a", "aac", "-b:a", "192k"},
		},
		{
			name:     "ts",
			format:   "ts",
			wantArgs: []string{"-c:v", "libx264", "-c:a", "aac", "-b:a", "192k"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := models.ConversionRequest{
				Source:       "source.mkv",
				TargetFormat: tt.format,
				Kind:         models.KindVideo,
				Mode:         models.ConversionReencode,
			}
			args, err := conversionArgs(req, "output."+tt.format)
			if err != nil {
				t.Fatalf("conversionArgs() error = %v", err)
			}
			for i := 0; i < len(tt.wantArgs); i += 2 {
				if !containsPair(args, tt.wantArgs[i], tt.wantArgs[i+1]) {
					t.Fatalf("conversionArgs() = %v, want pair %q %q", args, tt.wantArgs[i], tt.wantArgs[i+1])
				}
			}
			for _, forbidden := range tt.forbidden {
				if containsArg(args, forbidden) {
					t.Fatalf("conversionArgs() = %v, must not contain %q", args, forbidden)
				}
			}
		})
	}
}

func TestConversionArgsRemuxPreservesCopyMode(t *testing.T) {
	req := models.ConversionRequest{
		Source:       "source.mp4",
		TargetFormat: "mkv",
		Kind:         models.KindVideo,
		Mode:         models.ConversionRemux,
	}
	args, err := conversionArgs(req, "output.mkv")
	if err != nil {
		t.Fatalf("conversionArgs() error = %v", err)
	}
	want := []string{"-y", "-i", "source.mp4", "-progress", "pipe:1", "-nostats", "-c", "copy", "output.mkv"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("conversionArgs() = %v, want %v", args, want)
	}
}

func TestConversionArgsRejectsUnsupportedBitrate(t *testing.T) {
	req := models.ConversionRequest{
		Source:           "source.mp4",
		TargetFormat:     "mp3",
		Kind:             models.KindAudio,
		AudioBitrateKbps: 321,
	}
	if _, err := conversionArgs(req, "output.mp3"); err == nil {
		t.Fatal("conversionArgs() error = nil, want bitrate validation error")
	}
}

func TestCanRemuxUsesConservativeExtensionFallback(t *testing.T) {
	service := &FFmpegService{}
	tests := []struct {
		name   string
		source string
		target string
		want   bool
	}{
		{name: "mp4 to mkv", source: "source.mp4", target: "mkv", want: true},
		{name: "webm to mkv", source: "source.webm", target: "mkv", want: true},
		{name: "webm to mp4", source: "source.webm", target: "mp4", want: false},
		{name: "unknown mkv codecs to webm", source: "source.mkv", target: "webm", want: false},
		{name: "avi to ts", source: "source.avi", target: "ts", want: false},
		{name: "same container", source: "source.mov", target: ".MOV", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := service.CanRemux(tt.source, tt.target); got != tt.want {
				t.Fatalf("CanRemux(%q, %q) = %t, want %t", tt.source, tt.target, got, tt.want)
			}
		})
	}
}

func TestCodecsFitContainerRejectsIncompatibleWebMStreams(t *testing.T) {
	compatible := []probedStream{
		{CodecType: "video", CodecName: "vp9"},
		{CodecType: "audio", CodecName: "opus"},
	}
	if !codecsFitContainer(compatible, "webm") {
		t.Fatal("codecsFitContainer() = false for VP9/Opus WebM streams")
	}

	incompatible := []probedStream{
		{CodecType: "video", CodecName: "h264"},
		{CodecType: "audio", CodecName: "aac"},
	}
	if codecsFitContainer(incompatible, "webm") {
		t.Fatal("codecsFitContainer() = true for H.264/AAC WebM streams")
	}
}

func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func containsPair(args []string, key, value string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] != key {
			continue
		}
		if value == "" {
			return true
		}
		return i+1 < len(args) && args[i+1] == value
	}
	return false
}

func TestNormalizeFormatTrimsWhitespaceAndLeadingDot(t *testing.T) {
	if got := normalizeFormat(" .WebM "); got != "webm" {
		t.Fatalf("normalizeFormat() = %q, want %q", got, "webm")
	}
	if strings.TrimSpace(normalizeFormat("")) != "" {
		t.Fatal("normalizeFormat(\"\") should be empty")
	}
}
