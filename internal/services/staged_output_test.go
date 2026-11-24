package services

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSingleStagedMediaFileFindsPlaylistSubdir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "My Playlist")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(sub, "01 - Episode.mp4")
	if err := os.WriteFile(media, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "cover.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := singleStagedMediaFile(dir)
	if err != nil || got != media {
		t.Fatalf("singleStagedMediaFile = %q, %v; want %q", got, err, media)
	}
}

func TestSingleStagedMediaFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("cover.jpg")
	write("subs.srt")
	write("video.mp4.part")
	write("video.mp4")

	if !isStagedMediaFile(filepath.Join(dir, "video.mp4")) {
		t.Fatal("the final media file was rejected")
	}
	for _, name := range []string{"cover.jpg", "subs.srt", "video.mp4.part"} {
		if isStagedMediaFile(filepath.Join(dir, name)) {
			t.Fatalf("%s was accepted as a final media file", name)
		}
	}
	got, err := singleStagedMediaFile(dir)
	if err != nil {
		t.Fatalf("singleStagedMediaFile: %v", err)
	}
	if want := filepath.Join(dir, "video.mp4"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	if err := os.Remove(filepath.Join(dir, "video.mp4")); err != nil {
		t.Fatal(err)
	}
	if _, err := singleStagedMediaFile(dir); err == nil {
		t.Fatal("expected failure when only sidecars and .part remain")
	}

	write("second.webm")
	write("first.mkv")
	if _, err := singleStagedMediaFile(dir); err == nil {
		t.Fatal("expected failure when multiple media files remain")
	}
}
