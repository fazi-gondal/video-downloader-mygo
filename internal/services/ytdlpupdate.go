package services

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/tools"
	"github.com/fazi-gondal/video-downloader-mygo/internal/ytdlpupdate"
)

// DefaultYtDlpManifestBaseURL is the project-owned signed update channel.
// Users can point Settings → Tools at another channel; the public key remains
// the trust anchor.
const DefaultYtDlpManifestBaseURL = "https://github.com/fazi-gondal/video-downloader-mygo/releases/download/yt-dlp-channel"

// This public key corresponds to the private key kept outside the repository
// and supplied to the yt-dlp channel workflow as YTDLP_MANIFEST_PRIVATE_KEY.
const defaultYtDlpManifestPublicKey = "05kcvsShTqh0+52Ur9v7v2uXpTC2o92OPbgH+M+MDUk="

const (
	maxYtDlpManifestBytes  = 1 << 20
	maxYtDlpSignatureBytes = 4 << 10
)

var (
	ErrYtDlpManifestUnavailable = errors.New("yt-dlp update manifest is unavailable")
	ErrYtDlpManifestSignature   = errors.New("yt-dlp update manifest signature is invalid")
	ErrYtDlpManifestFormat      = errors.New("yt-dlp update manifest is invalid")
)

// YtDlpUpdate is one available update.
type YtDlpUpdate struct {
	Version string
	File    ytdlpupdate.File
}

// YtDlpManifestPublicKey returns the embedded channel verification key.
func YtDlpManifestPublicKey() ed25519.PublicKey {
	key, err := base64.StdEncoding.DecodeString(defaultYtDlpManifestPublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil
	}
	return ed25519.PublicKey(key)
}

// NormalizeYtDlpManifestBaseURL applies the default and removes a trailing
// slash. It does not validate the scheme; CheckYtDlpUpdate does.
func NormalizeYtDlpManifestBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultYtDlpManifestBaseURL
	}
	return strings.TrimRight(raw, "/")
}

// CheckYtDlpUpdate fetches, verifies, and selects the current platform's
// update. The manifest signature is checked before the JSON is parsed.
func CheckYtDlpUpdate(ctx context.Context, baseURL string, publicKey ed25519.PublicKey) (*YtDlpUpdate, error) {
	return checkYtDlpUpdate(ctx, baseURL, publicKey, nil)
}

func checkYtDlpUpdate(ctx context.Context, baseURL string, publicKey ed25519.PublicKey, client *http.Client) (*YtDlpUpdate, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	base := NormalizeYtDlpManifestBaseURL(baseURL)
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Scheme, "https") {
		return nil, fmt.Errorf("%w: source must be an https URL", ErrYtDlpManifestUnavailable)
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	data, err := fetchYtDlpManifestBytes(ctx, client, base+"/yt-dlp-manifest.json", maxYtDlpManifestBytes)
	if err != nil {
		return nil, err
	}
	signatureText, err := fetchYtDlpManifestBytes(ctx, client, base+"/yt-dlp-manifest.sig", maxYtDlpSignatureBytes)
	if err != nil {
		return nil, err
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signatureText)))
	if err != nil {
		return nil, fmt.Errorf("%w: signature is not base64", ErrYtDlpManifestFormat)
	}
	manifest, err := ytdlpupdate.Verify(data, signature, publicKey)
	if errors.Is(err, ytdlpupdate.ErrSignature) {
		return nil, fmt.Errorf("%w: %v", ErrYtDlpManifestSignature, err)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrYtDlpManifestFormat, err)
	}
	file, err := manifest.FileForCurrentPlatform()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrYtDlpManifestFormat, err)
	}
	return &YtDlpUpdate{Version: manifest.Version, File: file}, nil
}

func fetchYtDlpManifestBytes(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrYtDlpManifestUnavailable, err)
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrYtDlpManifestUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: HTTP %d", ErrYtDlpManifestUnavailable, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrYtDlpManifestUnavailable, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: response is too large", ErrYtDlpManifestFormat)
	}
	return data, nil
}

// YtDlpInstallOptions configures an update install.
type YtDlpInstallOptions struct {
	ManagedDir string
	Client     *http.Client
	Rename     func(oldpath, newpath string) error
}

// InstallYtDlpUpdate downloads, verifies, self-checks, and atomically installs
// the selected yt-dlp binary. The previous binary is restored on any handled
// failure or cancellation.
func InstallYtDlpUpdate(ctx context.Context, update *YtDlpUpdate, opts YtDlpInstallOptions, progress func(ToolInstallProgress)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if update == nil || update.Version == "" || update.File.URL == "" {
		return &ToolInstallError{Stage: ToolStageChecking, Name: "yt-dlp", Err: errors.New("empty update")}
	}
	if opts.ManagedDir == "" {
		return errors.New("managed tool directory is empty")
	}
	report := func(stage ToolInstallStage, name string, downloaded, total int64) {
		if progress != nil {
			progress(ToolInstallProgress{Stage: stage, Name: name, Downloaded: downloaded, Total: total})
		}
	}
	if err := os.MkdirAll(opts.ManagedDir, 0o700); err != nil {
		return &ToolInstallError{Stage: ToolStageInstalling, Name: "yt-dlp", Err: err}
	}
	_ = os.Chmod(opts.ManagedDir, 0o700)
	work, err := os.MkdirTemp(opts.ManagedDir, ".install-*")
	if err != nil {
		return &ToolInstallError{Stage: ToolStageInstalling, Name: "yt-dlp", Err: err}
	}
	defer os.RemoveAll(work)

	name := "yt-dlp"
	if runtime.GOOS == "windows" {
		name = "yt-dlp.exe"
	}
	target := filepath.Join(work, name)
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Minute}
	}
	report(ToolStageDownloading, "yt-dlp", 0, 0)
	if err := downloadTool(ctx, client, update.File.URL, update.File.SHA256, target, func(downloaded, total int64) {
		report(ToolStageDownloading, "yt-dlp", downloaded, total)
	}); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		stage := ToolStageDownloading
		if errors.Is(err, errToolChecksum) {
			stage = ToolStageVerifying
		}
		return &ToolInstallError{Stage: stage, Name: "yt-dlp", Err: err}
	}
	report(ToolStageVerifying, "yt-dlp", 0, 0)
	if err := os.Chmod(target, 0o755); err != nil {
		return &ToolInstallError{Stage: ToolStageInstalling, Name: "yt-dlp", Err: err}
	}
	report(ToolStageSelfCheck, "yt-dlp", 0, 0)
	version, err := selfCheckToolFlag(ctx, target, "--version")
	if err != nil {
		return &ToolInstallError{Stage: ToolStageSelfCheck, Name: "yt-dlp", Err: err}
	}
	if strings.TrimSpace(version) != update.Version {
		return &ToolInstallError{
			Stage: ToolStageSelfCheck,
			Name:  "yt-dlp",
			Err:   fmt.Errorf("reported version %q, want %q", strings.TrimSpace(version), update.Version),
		}
	}
	report(ToolStageInstalling, "yt-dlp", 0, 0)
	if err := installToolFiles(ctx, work, opts.ManagedDir, []string{name}, opts.Rename); err != nil {
		return &ToolInstallError{Stage: ToolStageInstalling, Name: "yt-dlp", Err: err}
	}
	return nil
}

// CompareYtDlpVersions exposes the version rule for the UI.
func CompareYtDlpVersions(a, b string) int {
	return tools.CompareVersions(a, b)
}
