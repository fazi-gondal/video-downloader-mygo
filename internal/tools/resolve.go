// Package tools resolves bundled yt-dlp / ffmpeg binaries shipped as MyGo resources.
package tools

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

var (
	once   sync.Once
	resDir string

	probeMu         sync.Mutex
	probeCache      = map[string]probeResult{}
	probeGeneration uint64

	overrideMu sync.RWMutex
	overrides  = map[string]string{}
)

type probeResult struct {
	ok       bool
	version  string
	timedOut bool
	expires  time.Time
}

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
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func bundledPath(name string) string {
	return lookIn(ResourcesDir(), name)
}

// managedDir is replaceable in tests; production uses the app-owned directory
// outside the .app bundle so an in-app install survives app replacement.
var managedDir = defaultManagedDir

// ManagedDir is where the app installs tools it downloads itself.
func ManagedDir() string {
	return managedDir()
}

func defaultManagedDir() string {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base, _ = os.UserConfigDir()
		}
		if base == "" {
			base, _ = os.UserHomeDir()
		}
		return filepath.Join(base, "VideoDownloader", "bin")
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "VideoDownloader", "bin")
	default:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".config", "VideoDownloader", "bin")
	}
}

func managedPath(name string) string {
	dir := ManagedDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, exeName(name))
}

// SetOverrides records optional user-provided binary paths. Empty values
// restore automatic selection. The override is only used after it passes the
// same runtime probe as every other candidate, so a mistyped path cannot
// disable a working tool. Call ResetProbeCache when the binary at an existing
// path may have changed.
func SetOverrides(ytdlpPath, ffmpegPath string) {
	overrideMu.Lock()
	overrides = map[string]string{}
	if p := strings.TrimSpace(ytdlpPath); p != "" {
		overrides["yt-dlp"] = p
	}
	if p := strings.TrimSpace(ffmpegPath); p != "" {
		overrides["ffmpeg"] = p
	}
	overrideMu.Unlock()
}

func overrideFor(name string) string {
	overrideMu.RLock()
	defer overrideMu.RUnlock()
	if p := overrides[name]; p != "" {
		return p
	}
	if name == "ffprobe" {
		if ffmpegPath := overrides["ffmpeg"]; ffmpegPath != "" {
			// A user-supplied ffmpeg normally ships with its ffprobe sibling.
			return filepath.Join(filepath.Dir(ffmpegPath), exeName("ffprobe"))
		}
	}
	return ""
}

// ResolveFast chooses an existing candidate without running it. Startup uses
// this so a slow Gatekeeper scan of yt-dlp cannot delay the window; an async
// tool check replaces the paths after probing.
func ResolveFast(name string) string {
	if p := overrideFor(name); p != "" && fileExists(p) {
		return p
	}
	if p := managedPath(name); p != "" && fileExists(p) {
		return p
	}
	if p := bundledPath(name); p != "" {
		return p
	}
	for _, p := range installedCandidates(name) {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// Resolve returns the first usable tool path. An explicit user override wins;
// otherwise a usable bundled binary is preferred, and finally the usual
// package-manager directories and PATH. A bundled binary can be present but
// broken (for example a dynamically linked FFmpeg whose Homebrew libraries are
// gone), so each candidate is run before it is accepted.
func Resolve(name string) string {
	if strings.EqualFold(name, "yt-dlp") {
		return resolveYtDlp()
	}
	if p := overrideFor(name); p != "" && usable(p, name) {
		return p
	}
	if p := managedPath(name); p != "" && usable(p, name) {
		return p
	}
	if p := bundledPath(name); p != "" && usable(p, name) {
		return p
	}
	for _, p := range installedCandidates(name) {
		if usable(p, name) {
			return p
		}
	}
	return ""
}

// resolveYtDlp applies one extra rule: when both a bundled and a system yt-dlp
// are usable, the newer version wins. yt-dlp's extractors change frequently,
// and a site fix should not require a new app release or a reinstall.
func resolveYtDlp() string {
	if p := chooseYtDlp(overrideFor("yt-dlp"), managedPath("yt-dlp"), bundledPath("yt-dlp"), installedCandidates("yt-dlp")); p != "" {
		return p
	}
	for _, p := range installedCandidates("youtube-dl") {
		if usable(p, "youtube-dl") {
			return p
		}
	}
	return ""
}

func chooseYtDlp(override, managed, bundled string, installed []string) string {
	if override != "" && usable(override, "yt-dlp") {
		return override
	}

	system := ""
	systemVersion := ""
	for _, p := range installed {
		if p == "" || p == managed || p == bundled {
			continue
		}
		result := probe(p, "yt-dlp")
		if !result.ok {
			continue
		}
		if system == "" || compareVersions(result.version, systemVersion) > 0 {
			system = p
			systemVersion = result.version
		}
	}

	best := ""
	bestVersion := ""
	seen := map[string]bool{}
	// managed, then bundled, then system: equal versions prefer the copy the
	// app owns, then the bundled copy, preserving the previous tie behavior.
	for _, p := range []string{managed, bundled, system} {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		result := probe(p, "yt-dlp")
		if !result.ok {
			continue
		}
		if best == "" || compareVersions(result.version, bestVersion) > 0 {
			best = p
			bestVersion = result.version
		}
	}
	if best != "" {
		return best
	}

	// No verified candidate: keep a bundled or managed binary that timed out
	// rather than disabling the tool for the session.
	for _, p := range []string{managed, bundled} {
		if p != "" && probe(p, "yt-dlp").timedOut {
			return p
		}
	}
	return ""
}

func installedCandidates(name string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	if runtime.GOOS != "windows" {
		// A packaged app launched from Finder has launchd's minimal PATH, so
		// look in the common package manager directories explicitly.
		for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"} {
			add(filepath.Join(dir, exeName(name)))
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		add(p)
	}
	return out
}

// ResetProbeCache forgets cached health probes, so Check again can pick up a
// tool installed while the app is running.
func ResetProbeCache() {
	probeMu.Lock()
	probeCache = map[string]probeResult{}
	probeGeneration++
	probeMu.Unlock()
}

// usable reports whether path runs, caching the answer per binary. The probe
// runs outside the lock so unrelated tools resolve concurrently.
func usable(path, name string) bool {
	return probe(path, name).ok
}

func probe(path, name string) probeResult {
	key := strings.ToLower(name) + "\x00" + path
	probeMu.Lock()
	cached, found := probeCache[key]
	generation := probeGeneration
	probeMu.Unlock()
	if found && (cached.expires.IsZero() || time.Now().Before(cached.expires)) {
		return cached
	}

	version, ok, timedOut := runVersion(path, name)
	result := probeResult{ok: ok, version: version, timedOut: timedOut}
	if timedOut {
		// A probe that ran out of time says nothing about the binary; retry
		// later instead of disabling the tool for the session, but do not
		// re-run an expensive probe on every Resolve in the meantime.
		result.expires = time.Now().Add(30 * time.Second)
	}

	probeMu.Lock()
	// A reset (Check again) started while this probe was running; do not
	// repopulate the new cache with a result from the old generation.
	if generation == probeGeneration {
		probeCache[key] = result
	}
	probeMu.Unlock()
	return result
}

func probeVersion(path, name string) string {
	return probe(path, name).version
}

func runVersion(path, name string) (version string, ok bool, timedOut bool) {
	flag := "-version"
	timeout := 5 * time.Second
	switch strings.ToLower(name) {
	case "yt-dlp", "youtube-dl":
		flag = "--version"
		// The yt-dlp launcher is a large self-extracting binary; a cold start
		// under Gatekeeper or a loaded machine can take many seconds.
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, flag)
	// A launcher can spawn a child that inherits stdout/stderr; without
	// WaitDelay, Run would wait for that child even after the deadline.
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", false, true
	}
	line := strings.TrimSpace(strings.SplitN(out.String(), "\n", 2)[0])
	if err != nil {
		// A loader error such as "dyld: …" is not a version.
		return "", false, false
	}
	return shortVersion(name, line), true, false
}

func shortVersion(name, line string) string {
	fields := strings.Fields(strings.TrimSpace(line))
	switch strings.ToLower(name) {
	case "ffmpeg", "ffprobe":
		if len(fields) >= 3 && strings.EqualFold(fields[1], "version") {
			return fields[2]
		}
	}
	if len(fields) > 0 {
		return fields[0]
	}
	return ""
}

func compareVersions(a, b string) int {
	ap, bp := versionNumbers(a), versionNumbers(b)
	for i := 0; i < len(ap) || i < len(bp); i++ {
		var av, bv int
		if i < len(ap) {
			av = ap[i]
		}
		if i < len(bp) {
			bv = bp[i]
		}
		if av != bv {
			if av > bv {
				return 1
			}
			return -1
		}
	}
	return 0
}

func versionNumbers(version string) []int {
	var numbers []int
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		n, err := strconv.Atoi(current.String())
		if err == nil {
			numbers = append(numbers, n)
		}
		current.Reset()
	}
	for _, r := range version {
		if r >= '0' && r <= '9' {
			current.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return numbers
}

func FFmpeg() string      { return Resolve("ffmpeg") }
func FFprobe() string     { return Resolve("ffprobe") }
func FFmpegFast() string  { return ResolveFast("ffmpeg") }
func FFprobeFast() string { return ResolveFast("ffprobe") }
func YtDlpFast() string   { return ResolveFast("yt-dlp") }

func YtDlp() string {
	if p := resolveYtDlp(); p != "" {
		return p
	}
	// A probe can fail on a cold start; an app-managed or bundled binary is
	// still a better guess than the bare name, which a Finder-launched app
	// cannot find on PATH.
	if p := managedPath("yt-dlp"); p != "" {
		return p
	}
	if p := bundledPath("yt-dlp"); p != "" {
		return p
	}
	return ""
}
