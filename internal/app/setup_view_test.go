package app

import (
	"testing"

	"github.com/fazi-gondal/video-downloader-mygo/internal/tools"
)

func TestRequireToolsOpensSetup(t *testing.T) {
	a := &App{page: PageConverter}
	if a.requireTools(true, false) {
		t.Fatal("requireTools succeeded without tools")
	}
	if a.page != PageDashboard {
		t.Fatalf("page = %q, want Home", a.page)
	}
	if a.setupDismissed {
		t.Fatal("blocked action left the setup card dismissed")
	}
}

func TestRequireToolsUsesProbeResults(t *testing.T) {
	a := &App{
		page: PageConverter,
		toolChecks: []tools.Check{
			{Name: "yt-dlp", OK: true},
			{Name: "ffmpeg", OK: true},
			{Name: "ffprobe", OK: false},
		},
	}
	if !a.requireTools(true, false) {
		t.Fatal("download should only require ffmpeg and yt-dlp")
	}
	a.page = PageConverter
	if a.requireTools(false, true) {
		t.Fatal("conversion should require ffprobe too")
	}
	a.toolChecks = []tools.Check{
		{Name: "yt-dlp", OK: false},
		{Name: "ffmpeg", OK: true},
		{Name: "ffprobe", OK: true},
	}
	if !a.requireTools(false, true) {
		t.Fatal("conversion should not require yt-dlp")
	}
	if a.page != PageDashboard {
		t.Fatalf("page = %q, want Home", a.page)
	}
}

func TestRequireToolsBlocksMissingYtDlp(t *testing.T) {
	a := &App{
		page: PageDownloads,
		toolChecks: []tools.Check{
			{Name: "yt-dlp", OK: false},
			{Name: "ffmpeg", OK: true},
			{Name: "ffprobe", OK: true},
		},
	}
	if a.requireTools(true, false) {
		t.Fatal("missing yt-dlp was not blocked")
	}
	if a.page != PageDashboard {
		t.Fatalf("page = %q, want Home", a.page)
	}
}

func TestToolReadyTreatsTimeoutAsUsable(t *testing.T) {
	a := &App{toolChecks: []tools.Check{
		{Name: "yt-dlp", TimedOut: true},
		{Name: "ffmpeg", TimedOut: true},
		{Name: "ffprobe", TimedOut: true},
	}}
	if !a.requireTools(true, true) {
		t.Fatal("timed-out probes should not block actions")
	}
}

func TestSetupVisible(t *testing.T) {
	a := &App{}
	if a.setupVisible() {
		t.Fatal("setup is visible before any probe")
	}
	a.toolChecks = []tools.Check{
		{Name: "yt-dlp", OK: true},
		{Name: "ffmpeg", OK: false},
		{Name: "ffprobe", OK: false},
	}
	if !a.setupVisible() {
		t.Fatal("setup should be visible while a tool is missing")
	}
	a.setupDismissed = true
	if a.setupVisible() {
		t.Fatal("dismissed setup should stay hidden")
	}
	a.setupDismissed = false
	a.toolChecks = []tools.Check{{Name: "yt-dlp", OK: true}, {Name: "ffmpeg", OK: true}, {Name: "ffprobe", OK: true}}
	if a.setupVisible() {
		t.Fatal("setup should be hidden when every tool is usable")
	}
}
