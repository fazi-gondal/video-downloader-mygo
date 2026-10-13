// Package tools resolves bundled yt-dlp / ffmpeg binaries shipped as MyGo resources.
package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/egoist/mygo"
)

var (
	once   sync.Once
	resDir string
)

// ResourcesDir is the app's resources folder (Contents/Resources on macOS,
// next to the executable elsewhere, or ./resources under go run).
func ResourcesDir() string {
	once.Do(func() {
		if d, err := mygo.App.Path(mygo.PathResources); err == nil && d != "" {
			resDir = d
			return
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
	// keep the flat lookup above and add this fallback for go run/test.
	platformDir := filepath.Join(dir, runtime.GOOS+"-"+runtime.GOARCH)
	candidates = append(candidates,
		filepath.Join(platformDir, "bin", exeName(name)),
		filepath.Join(platformDir, "bin", name),
	)
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// Resolve returns the path of a tool: bundled resource first, then PATH.
func Resolve(name string) string {
	if p := lookIn(ResourcesDir(), name); p != "" {
		return p
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath(name + ".exe"); err == nil {
			return p
		}
	}
	return ""
}

func FFmpeg() string  { return Resolve("ffmpeg") }
func FFprobe() string { return Resolve("ffprobe") }

func YtDlp() string {
	if p := Resolve("yt-dlp"); p != "" {
		return p
	}
	return Resolve("youtube-dl")
}
