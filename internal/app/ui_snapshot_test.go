package app

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/fazi-gondal/video-downloader-mygo/internal/config"
	appui "github.com/fazi-gondal/video-downloader-mygo/internal/ui"
)

func saveSnapshot(t *testing.T, name string, tt *ui.Tester) {
	t.Helper()
	dir := os.Getenv("UI_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set UI_SNAPSHOT_DIR to write render snapshots")
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	t.Logf("snapshot: %s", path)
}

func TestTopBarSnapshot(t *testing.T) {
	a := &App{page: PageDashboard, lang: "zh-Hans"}
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(appui.NativeTheme(true))
		ui.Column(c).Grow(1).Children(func() {
			a.topBar(c)
		})
	}, 900, 80)
	tt.Frame()
	saveSnapshot(t, "topbar.png", tt)
}

func TestSettingsSnapshot(t *testing.T) {
	a := &App{lang: "zh-Hans", settingsDraft: config.DefaultSettings()}
	a.settingsDraft.Language = "zh-Hans"
	a.maxConcurrentSlider = 4
	a.concurrentFragmentsSlider = 1
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(appui.NativeTheme(true))
		ui.Column(c).Grow(1).Padding(16).Children(func() {
			a.refinedSettingsPage(c)
		})
	}, 980, 900)
	tt.SetLocale("zh-Hans")
	tt.Frame()
	saveSnapshot(t, "settings.png", tt)
}

func heroShell(a *App, dark bool) func(c *ui.Context) {
	return func(c *ui.Context) {
		c.SetTheme(appui.NativeTheme(dark))
		ui.Row(c).Fill().Children(func() {
			ui.Column(c).Width(212).FillHeight()
			ui.Column(c).Grow(1).FillHeight().Padding(16).Children(func() {
				a.dashboardHero(c)
			})
		})
	}
}

func TestHeroSnapshot(t *testing.T) {
	a := &App{lang: "zh-Hans"}
	tt := ui.NewTester(heroShell(a, false), 1120, 680)
	tt.SetLocale("zh-Hans")
	tt.Frame()
	saveSnapshot(t, "hero.png", tt)
}

func TestHeroWithExtractorSnapshot(t *testing.T) {
	a := sampleExtractApp()
	a.lang = "zh-Hans"
	a.extractOpen = true
	tt := ui.NewTester(heroShell(a, false), 1120, 680)
	tt.SetLocale("zh-Hans")
	tt.Frame()
	saveSnapshot(t, "hero-open.png", tt)
}

func TestHeroNarrowSnapshot(t *testing.T) {
	a := sampleExtractApp()
	a.lang = "zh-Hans"
	a.extractOpen = true
	tt := ui.NewTester(heroShell(a, false), 800, 560)
	tt.SetLocale("zh-Hans")
	tt.Frame()
	saveSnapshot(t, "hero-narrow.png", tt)
}

func TestHeroDarkSnapshot(t *testing.T) {
	a := &App{lang: "zh-Hans"}
	tt := ui.NewTester(heroShell(a, true), 1120, 680)
	tt.SetLocale("zh-Hans")
	tt.Frame()
	saveSnapshot(t, "hero-dark.png", tt)
}
