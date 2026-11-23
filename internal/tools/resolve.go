// Package tools resolves bundled yt-dlp / ffmpeg binaries shipped as MyGo resources.
package tools

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

var (
	once   sync.Once
	resDir string

	healthMu sync.Mutex
	healthOK = map[string]bool{}
)

// ResourcesDir is the app's resources folder (Contents/Resources on macOS,
// next to the executable elsewhere, or ./resources under go run).
func ResourcesDir() string {
	once.Do(func() {
		if d, err := mygo.App.Path(mygo.PathResources); err == nil && d != "" {
			if st, err := os.Stat(d); err == nil && st.IsDir() {
				resDir = d
				return
			}
		}
		if exe, err := os.Executable(); err == nil {
			candidate := filepath.Join(filepath.Dir(exe), "resources")
			if st, err := os.Stat(candidate); err == nil && st.IsDir() {
				resDir = candidate
				return
			}
		}
		if wd, err := os.Getwd(); err == nil {
			dir := wd
			for {
				candidate := filepath.Join(dir, "resources")
				if st, err := os.Stat(candidate); err == nil && st.IsDir() {
					resDir = candidate
					return
				}
				parent := filepath.Dir(dir)
				if parent == dir {
					break
				}
				dir = parent
			}
		}
		if exe, err := os.Executable(); err == nil {
			resDir = filepath.Join(filepath.Dir(exe), "resources")
		}
	})
	return resDir
}

func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func lookIn(dir, name string) string {
	if dir == "" {
		return ""
	}
	platformDir := filepath.Join(dir, runtime.GOOS+"-"+runtime.GOARCH)
	candidates := make([]string, 0, 8)
	for _, root := range []string{dir, platformDir} {
		candidates = append(candidates,
			filepath.Join(root, "bin", exeName(name)),
			filepath.Join(root, exeName(name)),
			filepath.Join(root, "bin", name),
			filepath.Join(root, name),
		)
	}
	// During development the checked-in resources retain their platform
	// directory. MyGo flattens the matching directory in packaged apps, so
	// try the flat layout first and the platform layout second.
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// Resolve returns the first usable path of a tool: bundled resource first,
// then the usual install directories, then PATH. A bundled binary can be
// present but broken (for example a dynamically linked FFmpeg whose Homebrew
// libraries are gone), so each candidate is probed by running it before it is
// accepted.
func Resolve(name string) string {
	for _, p := range resolveCandidates(name) {
		if p == "" {
			continue
		}
		if usable(p, name) {
			return p
		}
	}
	return ""
}

func resolveCandidates(name string) []string {
	var out []string
	if p := lookIn(ResourcesDir(), name); p != "" {
		out = append(out, p)
	}
	if runtime.GOOS != "windows" {
		// A packaged app launched from Finder has launchd's minimal PATH, so
		// look in the common package manager directories explicitly.
		for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"} {
			out = append(out, filepath.Join(dir, exeName(name)))
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		out = append(out, p)
	}
	return out
}

// ResetProbeCache forgets cached health probes, so Check again can pick up a
// tool installed while the app is running.
func ResetProbeCache() {
	healthMu.Lock()
	healthOK = map[string]bool{}
	healthMu.Unlock()
}

// usable reports whether path runs, caching the answer per binary. The probe
// runs outside the lock so unrelated tools resolve concurrently.
func usable(path, name string) bool {
	healthMu.Lock()
	ok, found := healthOK[path]
	healthMu.Unlock()
	if found {
		return ok
	}

	ok, timedOut := runVersion(path, name)
	if timedOut {
		// A probe that ran out of time says nothing about the binary; let a
		// later call try again instead of disabling the tool for the session.
		return false
	}

	healthMu.Lock()
	healthOK[path] = ok
	healthMu.Unlock()
	return ok
}

func runVersion(path, name string) (ok bool, timedOut bool) {
	flag := "-version"
	timeout := 3 * time.Second
	switch strings.ToLower(name) {
	case "yt-dlp", "youtube-dl":
		flag = "--version"
		// The yt-dlp launcher is a large self-extracting binary; a cold start
		// under Gatekeeper or a loaded machine can take several seconds.
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, flag)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return false, true
	}
	return err == nil, false
}

func FFmpeg() string  { return Resolve("ffmpeg") }
func FFprobe() string { return Resolve("ffprobe") }

func YtDlp() string {
	if p := Resolve("yt-dlp"); p != "" {
		return p
	}
	// A probe can fail on a cold start; the bundled binary is still a better
	// guess than the bare name, which a Finder-launched app cannot find on PATH.
	if p := lookIn(ResourcesDir(), "yt-dlp"); p != "" {
		return p
	}
	return Resolve("youtube-dl")
}
