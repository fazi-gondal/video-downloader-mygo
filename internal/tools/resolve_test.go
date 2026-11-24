package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLookInPlatformResourceDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	platformBin := filepath.Join(root, runtime.GOOS+"-"+runtime.GOARCH, "bin")
	if err := os.MkdirAll(platformBin, 0o755); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(platformBin, exeName("ffmpeg"))
	if err := os.WriteFile(want, []byte("test binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := lookIn(root, "ffmpeg"); got != want {
		t.Fatalf("lookIn(%q, %q) = %q, want %q", root, "ffmpeg", got, want)
	}
}

func TestLookInPrefersFlattenedPackagedResource(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	flatBin := filepath.Join(root, "bin")
	platformBin := filepath.Join(root, runtime.GOOS+"-"+runtime.GOARCH, "bin")
	if err := os.MkdirAll(flatBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(platformBin, 0o755); err != nil {
		t.Fatal(err)
	}

	flat := filepath.Join(flatBin, exeName("ffprobe"))
	platform := filepath.Join(platformBin, exeName("ffprobe"))
	if err := os.WriteFile(flat, []byte("flat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(platform, []byte("platform"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := lookIn(root, "ffprobe"); got != flat {
		t.Fatalf("lookIn(%q, %q) = %q, want flattened path %q", root, "ffprobe", got, flat)
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"2026.11.23", "2026.08.19", 1},
		{"2026.08.19", "2026.11.23", -1},
		{"2026.08.19", "2026.08.19", 0},
		{"2026.8.9", "2026.08.19", -1},
		{"nightly2026.11.23", "2026.11.22", 1},
	}
	for _, tt := range tests {
		if got := compareVersions(tt.a, tt.b); got != tt.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestShortVersion(t *testing.T) {
	if got := shortVersion("ffmpeg", "ffmpeg version 7.1.1 Copyright (c) 2000-2025"); got != "7.1.1" {
		t.Fatalf("ffmpeg version = %q, want 7.1.1", got)
	}
	if got := shortVersion("yt-dlp", "2026.11.23"); got != "2026.11.23" {
		t.Fatalf("yt-dlp version = %q, want 2026.11.23", got)
	}
}

func TestChooseYtDlpPrefersNewerUsableSystemVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tool scripts are Unix-only")
	}
	ResetProbeCache()
	bundled := fakeTool(t, "2026.08.19")
	older := fakeTool(t, "2026.07.01")
	newer := fakeTool(t, "2026.11.23")
	broken := filepath.Join(t.TempDir(), "broken-ytdlp")
	if err := os.WriteFile(broken, []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := chooseYtDlp("", bundled, []string{older}); got != bundled {
		t.Fatalf("older system yt-dlp = %q, want bundled %q", got, bundled)
	}
	if got := chooseYtDlp("", bundled, []string{newer}); got != newer {
		t.Fatalf("newer system yt-dlp = %q, want %q", got, newer)
	}
	if got := chooseYtDlp("", bundled, []string{broken, newer}); got != newer {
		t.Fatalf("broken first candidate = %q, want the next usable %q", got, newer)
	}
	newest := fakeTool(t, "2026.12.01")
	if got := chooseYtDlp("", bundled, []string{older, broken, newest, newer}); got != newest {
		t.Fatalf("multiple system candidates = %q, want the newest %q", got, newest)
	}
	override := fakeTool(t, "2026.01.01")
	if got := chooseYtDlp(override, bundled, []string{newer}); got != override {
		t.Fatalf("explicit override = %q, want %q", got, override)
	}
}

func TestResolveUsesCustomOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tool scripts are Unix-only")
	}
	ResetProbeCache()
	t.Cleanup(func() {
		SetOverrides("", "")
		ResetProbeCache()
	})
	ytdlp := fakeTool(t, "2026.11.23")
	ffmpeg := fakeTool(t, "ffmpeg version 7.1.1")
	SetOverrides(ytdlp, ffmpeg)

	if got := ResolveFast("yt-dlp"); got != ytdlp {
		t.Fatalf("ResolveFast(yt-dlp) = %q, want %q", got, ytdlp)
	}
	if got := Resolve("yt-dlp"); got != ytdlp {
		t.Fatalf("Resolve(yt-dlp) = %q, want %q", got, ytdlp)
	}
	if got := Resolve("ffmpeg"); got != ffmpeg {
		t.Fatalf("Resolve(ffmpeg) = %q, want %q", got, ffmpeg)
	}
}

func fakeTool(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	script := fmt.Sprintf("#!/bin/sh\necho %s\n", version)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
