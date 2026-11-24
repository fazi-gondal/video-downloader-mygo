package ytdlpupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestVerifySignedManifest(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"version":"2026.12.01","files":{"darwin-arm64":{"url":"https://github.com/yt-dlp/yt-dlp/releases/download/2026.12.01/yt-dlp_macos","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":123}}}`)
	signature := Sign(data, priv)

	manifest, err := Verify(data, signature, pub)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if manifest.Version != "2026.12.01" {
		t.Fatalf("Version = %q", manifest.Version)
	}
	file, err := manifest.FileFor("darwin", "arm64")
	if err != nil || !strings.Contains(file.URL, "yt-dlp_macos") {
		t.Fatalf("FileFor = %#v, %v", file, err)
	}
}

func TestVerifyRejectsTamperedManifest(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"version":"2026.12.01","files":{}}`)
	signature := Sign(data, priv)
	data = []byte(`{"version":"2026.12.02","files":{}}`)
	if _, err := Verify(data, signature, pub); !errors.Is(err, ErrSignature) {
		t.Fatalf("Verify error = %v, want ErrSignature", err)
	}
}

func TestVerifyRejectsUnsafeFile(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		Version: "2026.12.01",
		Files: map[string]File{
			"darwin-arm64": {
				URL:    "http://127.0.0.1/yt-dlp",
				SHA256: strings.Repeat("a", 64),
				Size:   1,
			},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(data, Sign(data, priv), pub); !errors.Is(err, ErrFormat) {
		t.Fatalf("Verify error = %v, want ErrFormat", err)
	}
}

func TestFileForMissingPlatform(t *testing.T) {
	manifest := Manifest{Files: map[string]File{"linux-amd64": {}}}
	if _, err := manifest.FileFor("plan9", "mips"); !errors.Is(err, ErrNoPlatformFile) {
		t.Fatalf("FileFor error = %v, want ErrNoPlatformFile", err)
	}
}
