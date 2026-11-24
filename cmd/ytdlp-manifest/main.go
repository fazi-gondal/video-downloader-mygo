// Command ytdlp-manifest publishes the signed yt-dlp update manifest.
//
// It downloads the selected upstream release assets, records their SHA-256
// values, signs the manifest with the channel private key, and writes
// yt-dlp-manifest.json and yt-dlp-manifest.sig. The GitHub workflow uploads
// those two files to the yt-dlp-channel release.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/ytdlpupdate"
)

const userAgent = "video-downloader-ytdlp-channel"

var platformAssets = map[string]string{
	"windows-amd64": "yt-dlp.exe",
	"windows-arm64": "yt-dlp_arm64.exe",
	"linux-amd64":   "yt-dlp_linux",
	"linux-arm64":   "yt-dlp_linux_aarch64",
	"darwin-amd64":  "yt-dlp_macos",
	"darwin-arm64":  "yt-dlp_macos",
}

func main() {
	tag := flag.String("tag", "", "yt-dlp release tag (default: latest)")
	repo := flag.String("repo", "yt-dlp/yt-dlp", "upstream GitHub repository")
	out := flag.String("out", "channel", "output directory")
	keyFile := flag.String("private-key", "", "file containing the base64 Ed25519 seed")
	keygen := flag.Bool("keygen", false, "generate a keypair and exit")
	flag.Parse()

	if *keygen {
		generateKey(*keyFile)
		return
	}
	privateKey := loadPrivateKey(*keyFile)
	if strings.TrimSpace(*tag) == "" {
		var err error
		*tag, err = latestTag(*repo)
		if err != nil {
			log.Fatalf("latest yt-dlp tag: %v", err)
		}
	}
	if *tag == "" {
		log.Fatal("empty yt-dlp tag")
	}
	manifest, err := buildManifest(*repo, *tag)
	if err != nil {
		log.Fatalf("build manifest: %v", err)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		log.Fatalf("encode manifest: %v", err)
	}
	data = append(data, '\n')
	signature := base64.StdEncoding.EncodeToString(ytdlpupdate.Sign(data, privateKey))
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatalf("create output directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(*out, "yt-dlp-manifest.json"), data, 0o644); err != nil {
		log.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(*out, "yt-dlp-manifest.sig"), []byte(signature+"\n"), 0o644); err != nil {
		log.Fatalf("write signature: %v", err)
	}
	fmt.Printf("published manifest for %s to %s\n", manifest.Version, *out)
}

func generateKey(path string) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatalf("generate key: %v", err)
	}
	seed := base64.StdEncoding.EncodeToString(priv.Seed())
	if path != "" {
		if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
			log.Fatalf("write private key: %v", err)
		}
		fmt.Printf("private key written to %s\n", path)
	} else {
		fmt.Printf("private %s\n", seed)
	}
	fmt.Printf("public %s\n", base64.StdEncoding.EncodeToString(pub))
}

func loadPrivateKey(path string) ed25519.PrivateKey {
	raw := os.Getenv("YTDLP_MANIFEST_PRIVATE_KEY")
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			log.Fatalf("read private key: %v", err)
		}
		raw = string(data)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(seed) != ed25519.SeedSize {
		log.Fatal("YTDLP_MANIFEST_PRIVATE_KEY must be a base64 Ed25519 seed")
	}
	return ed25519.NewKeyFromSeed(seed)
}

func latestTag(repo string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return "", err
	}
	return strings.TrimSpace(release.TagName), nil
}

func buildManifest(repo, tag string) (ytdlpupdate.Manifest, error) {
	client := &http.Client{Timeout: 30 * time.Minute}
	manifest := ytdlpupdate.Manifest{
		Version:     tag,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Files:       make(map[string]ytdlpupdate.File, len(platformAssets)),
	}
	cache := make(map[string]ytdlpupdate.File, len(platformAssets))
	for platform, asset := range platformAssets {
		if file, ok := cache[asset]; ok {
			manifest.Files[platform] = file
			continue
		}
		url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, asset)
		file, err := downloadAsset(client, url, asset)
		if err != nil {
			return ytdlpupdate.Manifest{}, fmt.Errorf("%s: %w", platform, err)
		}
		cache[asset] = file
		manifest.Files[platform] = file
	}
	return manifest, nil
}

func downloadAsset(client *http.Client, url, asset string) (ytdlpupdate.File, error) {
	tmp, err := os.CreateTemp("", ".ytdlp-channel-*")
	if err != nil {
		return ytdlpupdate.File{}, err
	}
	path := tmp.Name()
	defer os.Remove(path)
	hash := sha256.New()
	written := &countingWriter{writer: io.MultiWriter(tmp, hash), label: asset}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		_ = tmp.Close()
		return ytdlpupdate.File{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		_ = tmp.Close()
		return ytdlpupdate.File{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = tmp.Close()
		return ytdlpupdate.File{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if _, err := io.Copy(written, resp.Body); err != nil {
		_ = tmp.Close()
		return ytdlpupdate.File{}, err
	}
	if err := tmp.Close(); err != nil {
		return ytdlpupdate.File{}, err
	}
	return ytdlpupdate.File{
		URL:    url,
		SHA256: hex.EncodeToString(hash.Sum(nil)),
		Size:   written.n,
	}, nil
}

type countingWriter struct {
	writer io.Writer
	label  string
	n      int64
	next   int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.n += int64(n)
	if w.next == 0 {
		w.next = 10 << 20
	}
	if w.n >= w.next {
		fmt.Fprintf(os.Stderr, "%s: %d MiB\n", w.label, w.n>>20)
		for w.n >= w.next {
			w.next += 10 << 20
		}
	}
	return n, err
}
