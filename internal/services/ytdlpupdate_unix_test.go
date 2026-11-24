//go:build !windows

package services

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fazi-gondal/video-downloader-mygo/internal/ytdlpupdate"
)

func TestYtDlpUpdateCheckAndInstall(t *testing.T) {
	pub, server, update := testYtDlpUpdateServer(t, "2026.12.01", []byte("#!/bin/sh\necho 2026.12.01\n"))
	defer server.Close()

	got, err := checkYtDlpUpdate(t.Context(), server.URL, pub, server.Client())
	if err != nil {
		t.Fatalf("checkYtDlpUpdate: %v", err)
	}
	if got.Version != update.Version || got.File.URL != update.File.URL {
		t.Fatalf("update = %#v, want %#v", got, update)
	}

	managed := t.TempDir()
	if err := InstallYtDlpUpdate(t.Context(), got, YtDlpInstallOptions{ManagedDir: managed, Client: server.Client()}, nil); err != nil {
		t.Fatalf("InstallYtDlpUpdate: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(managed, "yt-dlp"))
	if err != nil || !strings.Contains(string(data), "2026.12.01") {
		t.Fatalf("installed yt-dlp = %q, err=%v", data, err)
	}
}

func TestYtDlpUpdateRejectsTamperedSignature(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/yt-dlp-manifest.json":
			_, _ = w.Write([]byte(`{"version":"2026.12.01","files":{}}`))
		case "/yt-dlp-manifest.sig":
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte("short"))))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if _, err := checkYtDlpUpdate(t.Context(), server.URL, pub, server.Client()); !errors.Is(err, ErrYtDlpManifestSignature) {
		t.Fatalf("check error = %v, want ErrYtDlpManifestSignature", err)
	}
}

func TestYtDlpUpdateRejectsVersionMismatch(t *testing.T) {
	pub, server, _ := testYtDlpUpdateServer(t, "2026.12.01", []byte("#!/bin/sh\necho 2026.11.01\n"))
	defer server.Close()
	update, err := checkYtDlpUpdate(t.Context(), server.URL, pub, server.Client())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	managed := writeOldYtDlp(t)
	err = InstallYtDlpUpdate(t.Context(), update, YtDlpInstallOptions{ManagedDir: managed, Client: server.Client()}, nil)
	var installErr *ToolInstallError
	if !errors.As(err, &installErr) || installErr.Stage != ToolStageSelfCheck {
		t.Fatalf("install error = %v, want self-check ToolInstallError", err)
	}
	assertOldYtDlp(t, managed)
}

func TestYtDlpUpdateReplaceFailureRollsBack(t *testing.T) {
	pub, server, _ := testYtDlpUpdateServer(t, "2026.12.01", []byte("#!/bin/sh\necho 2026.12.01\n"))
	defer server.Close()
	update, err := checkYtDlpUpdate(t.Context(), server.URL, pub, server.Client())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	managed := writeOldYtDlp(t)
	err = InstallYtDlpUpdate(t.Context(), update, YtDlpInstallOptions{
		ManagedDir: managed,
		Client:     server.Client(),
		Rename:     func(oldpath, newpath string) error { return errors.New("simulated replace failure") },
	}, nil)
	var installErr *ToolInstallError
	if !errors.As(err, &installErr) || installErr.Stage != ToolStageInstalling {
		t.Fatalf("install error = %v, want install-stage ToolInstallError", err)
	}
	assertOldYtDlp(t, managed)
}

func TestYtDlpUpdateCancellationKeepsOld(t *testing.T) {
	pub, server, _ := testYtDlpUpdateServer(t, "2026.12.01", []byte("#!/bin/sh\necho 2026.12.01\n"))
	defer server.Close()
	update, err := checkYtDlpUpdate(t.Context(), server.URL, pub, server.Client())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	managed := writeOldYtDlp(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = InstallYtDlpUpdate(ctx, update, YtDlpInstallOptions{ManagedDir: managed, Client: server.Client()}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("install error = %v, want context.Canceled", err)
	}
	assertOldYtDlp(t, managed)
}

func testYtDlpUpdateServer(t *testing.T, version string, binary []byte) (ed25519.PublicKey, *httptest.Server, *YtDlpUpdate) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var manifestData, signature []byte
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/yt-dlp-manifest.json":
			_, _ = w.Write(manifestData)
		case "/yt-dlp-manifest.sig":
			_, _ = w.Write(signature)
		case "/yt-dlp":
			_, _ = w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	server.StartTLS()

	sum := sha256.Sum256(binary)
	manifest := ytdlpupdate.Manifest{
		Version: version,
		Files: map[string]ytdlpupdate.File{
			ytdlpupdate.CurrentPlatformKey(): {
				URL:    server.URL + "/yt-dlp",
				SHA256: hex.EncodeToString(sum[:]),
				Size:   int64(len(binary)),
			},
		},
	}
	manifestData, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature = []byte(base64.StdEncoding.EncodeToString(ytdlpupdate.Sign(manifestData, priv)))
	update := &YtDlpUpdate{Version: version, File: manifest.Files[ytdlpupdate.CurrentPlatformKey()]}
	return pub, server, update
}

func writeOldYtDlp(t *testing.T) string {
	t.Helper()
	managed := t.TempDir()
	if err := os.WriteFile(filepath.Join(managed, "yt-dlp"), []byte("old yt-dlp"), 0o755); err != nil {
		t.Fatal(err)
	}
	return managed
}

func assertOldYtDlp(t *testing.T, managed string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(managed, "yt-dlp"))
	if err != nil || string(data) != "old yt-dlp" {
		t.Fatalf("yt-dlp after failed install = %q, err=%v", data, err)
	}
}
