package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ToolInstallStage identifies where an install failed, so the UI can say
// whether the download, checksum, write, or self-check failed.
type ToolInstallStage string

const (
	ToolStagePreparing   ToolInstallStage = "preparing"
	ToolStageDownloading ToolInstallStage = "downloading"
	ToolStageVerifying   ToolInstallStage = "verifying"
	ToolStageInstalling  ToolInstallStage = "installing"
	ToolStageSelfCheck   ToolInstallStage = "self-check"
	ToolStageHomebrew    ToolInstallStage = "homebrew"
)

// ToolInstallProgress is sent to the UI while an install runs.
type ToolInstallProgress struct {
	Stage      ToolInstallStage
	Name       string
	Downloaded int64
	Total      int64
}

// ToolInstallSource pins the URLs and SHA-256 values. Checksums never come
// from the download response.
type ToolInstallSource struct {
	Version       string
	FFmpegURL     string
	FFmpegSHA256  string
	FFprobeURL    string
	FFprobeSHA256 string
}

// ToolInstallOptions configures one user-triggered install.
type ToolInstallOptions struct {
	ManagedDir string
	Homebrew   string
	Source     ToolInstallSource
	Client     *http.Client
	// Rename is injectable for rollback tests; nil uses os.Rename.
	Rename func(oldpath, newpath string) error
}

// ToolInstallError wraps a stage failure without losing the underlying error.
type ToolInstallError struct {
	Stage ToolInstallStage
	Name  string
	Err   error
}

func (e *ToolInstallError) Error() string {
	if e.Name != "" {
		return fmt.Sprintf("%s %s failed: %v", e.Stage, e.Name, e.Err)
	}
	return fmt.Sprintf("%s failed: %v", e.Stage, e.Err)
}

func (e *ToolInstallError) Unwrap() error { return e.Err }

// ErrToolInstallUnsupported means this platform has no in-app FFmpeg source.
var ErrToolInstallUnsupported = errors.New("automatic FFmpeg installation is not available on this platform")

const maxToolDownloadBytes = 256 << 20

var errToolChecksum = errors.New("checksum mismatch")

// HomebrewPath returns the Homebrew executable used by the install button, or
// empty when Homebrew is not available on this platform.
func HomebrewPath() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	for _, path := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	if path, err := exec.LookPath("brew"); err == nil {
		return path
	}
	return ""
}

// StaticToolSource returns the pinned static build for this Mac. Other
// platforms ship FFmpeg in the app package, so they have no download source.
func StaticToolSource() (ToolInstallSource, bool) {
	const base = "https://github.com/eugeneware/ffmpeg-static/releases/download/b6.1.1/"
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		return ToolInstallSource{
			Version:       "b6.1.1",
			FFmpegURL:     base + "ffmpeg-darwin-arm64",
			FFmpegSHA256:  "a90e3db6a3fd35f6074b013f948b1aa45b31c6375489d39e572bea3f18336584",
			FFprobeURL:    base + "ffprobe-darwin-arm64",
			FFprobeSHA256: "bb2db6f5d8cef919da12fbf592119a987202a8c060a886f3cab091f9cab90b64",
		}, true
	case "darwin/amd64":
		return ToolInstallSource{
			Version:       "b6.1.1",
			FFmpegURL:     base + "ffmpeg-darwin-x64",
			FFmpegSHA256:  "ebdddc936f61e14049a2d4b549a412b8a40deeff6540e58a9f2a2da9e6b18894",
			FFprobeURL:    base + "ffprobe-darwin-x64",
			FFprobeSHA256: "fa3add0ce901f7241abe0dfc0155d958fc834aca3f8ce61f87cc712ae669c1e0",
		}, true
	}
	return ToolInstallSource{}, false
}

// InstallFFmpeg installs ffmpeg and ffprobe into ManagedDir with Homebrew, or
// downloads the pinned static build when Homebrew is unavailable. Existing
// tools are replaced only after both downloads pass their checks and self
// checks; a failed replace restores the previous files.
func InstallFFmpeg(ctx context.Context, opts ToolInstallOptions, progress func(ToolInstallProgress)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.ManagedDir == "" {
		return errors.New("managed tool directory is empty")
	}
	report := func(stage ToolInstallStage, name string, downloaded, total int64) {
		if progress != nil {
			progress(ToolInstallProgress{Stage: stage, Name: name, Downloaded: downloaded, Total: total})
		}
	}

	report(ToolStagePreparing, "", 0, 0)
	if opts.Homebrew != "" {
		return installWithHomebrew(ctx, opts, report)
	}
	source := opts.Source
	if source.FFmpegURL == "" {
		var ok bool
		source, ok = StaticToolSource()
		if !ok {
			return ErrToolInstallUnsupported
		}
	}
	return installStatic(ctx, opts, source, report)
}

func installStatic(ctx context.Context, opts ToolInstallOptions, source ToolInstallSource, report func(ToolInstallStage, string, int64, int64)) error {
	if err := os.MkdirAll(opts.ManagedDir, 0o700); err != nil {
		return &ToolInstallError{Stage: ToolStageInstalling, Err: err}
	}
	_ = os.Chmod(opts.ManagedDir, 0o700)
	work, err := os.MkdirTemp(opts.ManagedDir, ".install-*")
	if err != nil {
		return &ToolInstallError{Stage: ToolStageInstalling, Err: err}
	}
	defer os.RemoveAll(work)

	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Minute}
	}
	files := []struct {
		name, url, sha, target string
	}{
		{"ffmpeg", source.FFmpegURL, source.FFmpegSHA256, filepath.Join(work, "ffmpeg")},
		{"ffprobe", source.FFprobeURL, source.FFprobeSHA256, filepath.Join(work, "ffprobe")},
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		report(ToolStageDownloading, file.name, 0, 0)
		if err := downloadTool(ctx, client, file.url, file.sha, file.target, func(downloaded, total int64) {
			report(ToolStageDownloading, file.name, downloaded, total)
		}); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if errors.Is(err, errToolChecksum) {
				return &ToolInstallError{Stage: ToolStageVerifying, Name: file.name, Err: err}
			}
			return &ToolInstallError{Stage: ToolStageDownloading, Name: file.name, Err: err}
		}
		report(ToolStageVerifying, file.name, 0, 0)
		if err := os.Chmod(file.target, 0o755); err != nil {
			return &ToolInstallError{Stage: ToolStageInstalling, Name: file.name, Err: err}
		}
		report(ToolStageSelfCheck, file.name, 0, 0)
		if err := selfCheckTool(ctx, file.target); err != nil {
			return &ToolInstallError{Stage: ToolStageSelfCheck, Name: file.name, Err: err}
		}
	}

	report(ToolStageInstalling, "", 0, 0)
	if err := installToolFiles(ctx, work, opts.ManagedDir, opts.Rename); err != nil {
		return &ToolInstallError{Stage: ToolStageInstalling, Err: err}
	}
	return nil
}

func downloadTool(ctx context.Context, client *http.Client, url, expectedSHA256, dest string, progress func(int64, int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxToolDownloadBytes {
		return errors.New("download exceeds size limit")
	}

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	hash := sha256.New()
	buf := make([]byte, 64*1024)
	var downloaded int64
	lastReport := time.Time{}
	for {
		if err := ctx.Err(); err != nil {
			_ = out.Close()
			_ = os.Remove(dest)
			return err
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			downloaded += int64(n)
			if downloaded > maxToolDownloadBytes {
				_ = out.Close()
				_ = os.Remove(dest)
				return errors.New("download exceeds size limit")
			}
			if _, err := out.Write(buf[:n]); err != nil {
				_ = out.Close()
				_ = os.Remove(dest)
				return err
			}
			_, _ = hash.Write(buf[:n])
			if progress != nil && (lastReport.IsZero() || time.Since(lastReport) >= 200*time.Millisecond) {
				progress(downloaded, resp.ContentLength)
				lastReport = time.Now()
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			_ = out.Close()
			_ = os.Remove(dest)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return readErr
		}
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dest)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dest)
		return err
	}
	if progress != nil {
		progress(downloaded, resp.ContentLength)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, strings.TrimSpace(expectedSHA256)) {
		_ = os.Remove(dest)
		return fmt.Errorf("%w: got %s", errToolChecksum, actual)
	}
	return nil
}

func selfCheckTool(ctx context.Context, path string) error {
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, path, "-version")
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if checkCtx.Err() != nil {
			return checkCtx.Err()
		}
		return err
	}
	return nil
}

func installToolFiles(ctx context.Context, work, managed string, rename func(string, string) error) error {
	if rename == nil {
		rename = os.Rename
	}
	names := []string{"ffmpeg", "ffprobe"}
	backups := make(map[string]string, len(names))
	var backedUp []string
	restoreBackups := func() {
		for _, dest := range backedUp {
			_ = os.Rename(backups[dest], dest)
		}
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			restoreBackups()
			return err
		}
		dest := filepath.Join(managed, name)
		info, err := os.Lstat(dest)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			restoreBackups()
			return err
		}
		if info.IsDir() {
			restoreBackups()
			return fmt.Errorf("%s is a directory", dest)
		}
		temp, err := os.CreateTemp(managed, name+".backup-*")
		if err != nil {
			restoreBackups()
			return err
		}
		backup := temp.Name()
		_ = temp.Close()
		_ = os.Remove(backup)
		if err := os.Rename(dest, backup); err != nil {
			restoreBackups()
			return err
		}
		backups[dest] = backup
		backedUp = append(backedUp, dest)
	}

	var replaced []string
	rollback := func() {
		for _, dest := range replaced {
			_ = os.Remove(dest)
		}
		restoreBackups()
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			rollback()
			return err
		}
		src := filepath.Join(work, name)
		dest := filepath.Join(managed, name)
		if err := rename(src, dest); err != nil {
			rollback()
			return err
		}
		replaced = append(replaced, dest)
	}
	for _, backup := range backups {
		_ = os.Remove(backup)
	}
	return nil
}

func installWithHomebrew(ctx context.Context, opts ToolInstallOptions, report func(ToolInstallStage, string, int64, int64)) error {
	report(ToolStageHomebrew, "ffmpeg", 0, 0)
	if err := runBrewInstall(ctx, opts.Homebrew); err != nil {
		return &ToolInstallError{Stage: ToolStageHomebrew, Name: "ffmpeg", Err: err}
	}
	prefix, err := brewPrefix(ctx, opts.Homebrew)
	if err != nil {
		return &ToolInstallError{Stage: ToolStageHomebrew, Name: "ffmpeg", Err: err}
	}
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		report(ToolStageSelfCheck, name, 0, 0)
		path := filepath.Join(prefix, "bin", name)
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return &ToolInstallError{Stage: ToolStageSelfCheck, Name: name, Err: fmt.Errorf("not found at %s", path)}
		}
		if err := selfCheckTool(ctx, path); err != nil {
			return &ToolInstallError{Stage: ToolStageSelfCheck, Name: name, Err: err}
		}
	}
	return nil
}

func runBrewInstall(ctx context.Context, brew string) error {
	cmd := exec.CommandContext(ctx, brew, "install", "ffmpeg")
	prepareBackgroundCmd(cmd)
	var diagnostics boundedDiagnosticBuffer
	diagnostics.limit = 64 << 10
	cmd.Stdout = io.Discard
	cmd.Stderr = &diagnostics
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s: %w", lastDiagnosticLine(diagnostics.String()), err)
		}
		return nil
	case <-ctx.Done():
		terminateProcessTree(cmd)
		<-done
		return ctx.Err()
	}
}

func brewPrefix(ctx context.Context, brew string) (string, error) {
	cmd := exec.CommandContext(ctx, brew, "--prefix")
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	prefix := strings.TrimSpace(string(out))
	if prefix == "" {
		return "", errors.New("brew returned an empty prefix")
	}
	return prefix, nil
}

func lastDiagnosticLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return "brew install failed"
}
