package tools

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// Check is the result of probing one bundled tool.
type Check struct {
	Name    string
	Path    string
	Version string
	OK      bool
}

// ProbeAll checks the tools the download pipeline depends on.
func ProbeAll() []Check {
	names := []string{"yt-dlp", "ffmpeg", "ffprobe"}
	checks := make([]Check, 0, len(names))
	for _, name := range names {
		checks = append(checks, Probe(name))
	}
	return checks
}

// Probe resolves a tool and reads its version, reporting whether it runs.
func Probe(name string) Check {
	check := Check{Name: name, Path: Resolve(name)}
	if check.Path == "" {
		return check
	}
	version, ok := toolVersion(check.Path, name)
	check.Version = version
	check.OK = ok
	return check
}

func toolVersion(path, name string) (string, bool) {
	flag := "-version"
	timeout := 5 * time.Second
	switch strings.ToLower(name) {
	case "yt-dlp", "youtube-dl":
		flag = "--version"
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, flag)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	line := strings.TrimSpace(strings.SplitN(out.String(), "\n", 2)[0])
	return line, err == nil
}
