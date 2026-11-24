//go:build !windows

package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestInstallStaticTools(t *testing.T) {
	managed := t.TempDir()
	source, server := testToolSource(t, []byte("#!/bin/sh\necho ffmpeg test\n"), []byte("#!/bin/sh\necho ffprobe test\n"))
	defer server.Close()

	err := InstallFFmpeg(t.Context(), ToolInstallOptions{
		ManagedDir: managed,
		Source:     source,
		Client:     server.Client(),
	}, nil)
	if err != nil {
		t.Fatalf("InstallFFmpeg: %v", err)
	}
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		path := filepath.Join(managed, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("%s is not executable", name)
		}
	}
}

func TestInstallStaticChecksumFailureKeepsOldTools(t *testing.T) {
	managed := writeOldTools(t)
	source, server := testToolSource(t, []byte("#!/bin/sh\nexit 0\n"), []byte("#!/bin/sh\nexit 0\n"))
	defer server.Close()
	source.FFmpegSHA256 = strings.Repeat("0", 64)

	err := InstallFFmpeg(t.Context(), ToolInstallOptions{
		ManagedDir: managed,
		Source:     source,
		Client:     server.Client(),
	}, nil)
	var installErr *ToolInstallError
	if !errors.As(err, &installErr) || installErr.Stage != ToolStageVerifying {
		t.Fatalf("InstallFFmpeg error = %v, want verify-stage ToolInstallError", err)
	}
	assertOldTools(t, managed)
	assertNoInstallWork(t, managed)
}

func TestInstallStaticCancellationKeepsOldTools(t *testing.T) {
	managed := writeOldTools(t)
	started := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	source := ToolInstallSource{
		FFmpegURL:     server.URL + "/ffmpeg",
		FFmpegSHA256:  strings.Repeat("0", 64),
		FFprobeURL:    server.URL + "/ffprobe",
		FFprobeSHA256: strings.Repeat("0", 64),
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- InstallFFmpeg(ctx, ToolInstallOptions{ManagedDir: managed, Source: source, Client: server.Client()}, nil)
	}()
	<-started
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("InstallFFmpeg error = %v, want context.Canceled", err)
	}
	assertOldTools(t, managed)
	assertNoInstallWork(t, managed)
}

func TestInstallStaticDownloadInterruptedKeepsOldTools(t *testing.T) {
	managed := writeOldTools(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("short"))
	}))
	defer server.Close()
	source := ToolInstallSource{
		FFmpegURL:     server.URL + "/ffmpeg",
		FFmpegSHA256:  strings.Repeat("0", 64),
		FFprobeURL:    server.URL + "/ffprobe",
		FFprobeSHA256: strings.Repeat("0", 64),
	}

	err := InstallFFmpeg(t.Context(), ToolInstallOptions{ManagedDir: managed, Source: source, Client: server.Client()}, nil)
	var installErr *ToolInstallError
	if !errors.As(err, &installErr) || installErr.Stage != ToolStageDownloading {
		t.Fatalf("InstallFFmpeg error = %v, want download-stage ToolInstallError", err)
	}
	assertOldTools(t, managed)
	assertNoInstallWork(t, managed)
}

func TestInstallStaticSelfCheckFailureKeepsOldTools(t *testing.T) {
	managed := writeOldTools(t)
	source, server := testToolSource(t, []byte("#!/bin/sh\nexit 7\n"), []byte("#!/bin/sh\nexit 0\n"))
	defer server.Close()

	err := InstallFFmpeg(t.Context(), ToolInstallOptions{
		ManagedDir: managed,
		Source:     source,
		Client:     server.Client(),
	}, nil)
	var installErr *ToolInstallError
	if !errors.As(err, &installErr) || installErr.Stage != ToolStageSelfCheck {
		t.Fatalf("InstallFFmpeg error = %v, want self-check ToolInstallError", err)
	}
	assertOldTools(t, managed)
	assertNoInstallWork(t, managed)
}

func TestInstallStaticReplaceFailureRollsBack(t *testing.T) {
	managed := writeOldTools(t)
	source, server := testToolSource(t, []byte("#!/bin/sh\nexit 0\n"), []byte("#!/bin/sh\nexit 0\n"))
	defer server.Close()

	err := InstallFFmpeg(t.Context(), ToolInstallOptions{
		ManagedDir: managed,
		Source:     source,
		Client:     server.Client(),
		Rename: func(oldpath, newpath string) error {
			if filepath.Base(newpath) == "ffprobe" {
				return errors.New("simulated replace failure")
			}
			return os.Rename(oldpath, newpath)
		},
	}, nil)
	var installErr *ToolInstallError
	if !errors.As(err, &installErr) || installErr.Stage != ToolStageInstalling {
		t.Fatalf("InstallFFmpeg error = %v, want install-stage ToolInstallError", err)
	}
	assertOldTools(t, managed)
	assertNoInstallWork(t, managed)
}

func TestInstallWithHomebrewFailureKeepsManagedTools(t *testing.T) {
	managed := writeOldTools(t)
	brew := filepath.Join(t.TempDir(), "brew")
	if err := os.WriteFile(brew, []byte("#!/bin/sh\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := InstallFFmpeg(t.Context(), ToolInstallOptions{ManagedDir: managed, Homebrew: brew}, nil)
	var installErr *ToolInstallError
	if !errors.As(err, &installErr) || installErr.Stage != ToolStageHomebrew {
		t.Fatalf("InstallFFmpeg error = %v, want homebrew ToolInstallError", err)
	}
	assertOldTools(t, managed)
}

func TestInstallWithHomebrewSuccess(t *testing.T) {
	prefix := t.TempDir()
	brew := filepath.Join(t.TempDir(), "brew")
	script := fmt.Sprintf(`#!/bin/sh
prefix=%q
case "$1" in
  install)
    mkdir -p "$prefix/bin"
    printf '#!/bin/sh\necho ffmpeg test\n' > "$prefix/bin/ffmpeg"
    printf '#!/bin/sh\necho ffprobe test\n' > "$prefix/bin/ffprobe"
    chmod +x "$prefix/bin/ffmpeg" "$prefix/bin/ffprobe"
    exit 0
    ;;
  --prefix)
    echo "$prefix"
    exit 0
    ;;
esac
exit 1
`, prefix)
	if err := os.WriteFile(brew, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	managed := t.TempDir()
	if err := InstallFFmpeg(t.Context(), ToolInstallOptions{ManagedDir: managed, Homebrew: brew}, nil); err != nil {
		t.Fatalf("InstallFFmpeg: %v", err)
	}
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := os.Stat(filepath.Join(prefix, "bin", name)); err != nil {
			t.Fatalf("brew output %s: %v", name, err)
		}
	}
}

func TestStaticToolSourceIsPinned(t *testing.T) {
	source, ok := StaticToolSource()
	if !ok {
		t.Skip("no static source for this platform")
	}
	if !strings.Contains(source.FFmpegURL, "/b6.1.1/") || !strings.Contains(source.FFprobeURL, "/b6.1.1/") {
		t.Fatalf("static source is not pinned: %+v", source)
	}
	if len(source.FFmpegSHA256) != 64 || len(source.FFprobeSHA256) != 64 {
		t.Fatalf("static source checksums are not SHA-256: %+v", source)
	}
}

func testToolSource(t *testing.T, ffmpeg, ffprobe []byte) (ToolInstallSource, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ffmpeg", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(ffmpeg)
	})
	mux.HandleFunc("/ffprobe", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(ffprobe)
	})
	server := httptest.NewServer(mux)
	return ToolInstallSource{
		FFmpegURL:     server.URL + "/ffmpeg",
		FFmpegSHA256:  sha256Text(ffmpeg),
		FFprobeURL:    server.URL + "/ffprobe",
		FFprobeSHA256: sha256Text(ffprobe),
	}, server
}

func sha256Text(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeOldTools(t *testing.T) string {
	t.Helper()
	managed := t.TempDir()
	for name, content := range map[string]string{"ffmpeg": "old ffmpeg", "ffprobe": "old ffprobe"} {
		if err := os.WriteFile(filepath.Join(managed, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return managed
}

func assertOldTools(t *testing.T, managed string) {
	t.Helper()
	for name, want := range map[string]string{"ffmpeg": "old ffmpeg", "ffprobe": "old ffprobe"} {
		data, err := os.ReadFile(filepath.Join(managed, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s after failed install = %q, err=%v; want %q", name, data, err, want)
		}
	}
}

func assertNoInstallWork(t *testing.T, managed string) {
	t.Helper()
	entries, err := os.ReadDir(managed)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".install-") {
			t.Fatalf("temporary install directory remains: %s", entry.Name())
		}
	}
}
