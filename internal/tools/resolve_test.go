package tools

import (
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

