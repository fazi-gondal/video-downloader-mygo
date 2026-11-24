package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/fazi-gondal/video-downloader-mygo/internal/services"
	"github.com/fazi-gondal/video-downloader-mygo/internal/tools"
	appui "github.com/fazi-gondal/video-downloader-mygo/internal/ui"
)

// setupVisible reports whether the first-run setup card should be shown.
func (a *App) setupVisible() bool {
	if a.setupDismissed {
		return false
	}
	if a.toolInstallRunning {
		return true
	}
	for _, check := range a.toolChecks {
		if !check.OK {
			return true
		}
	}
	return false
}

// setupCard is the focused first-run card on Home. It is the same card opened
// by a blocked download or conversion.
func (a *App) setupCard(c *ui.Context) {
	ui.Column(c).FillWidth().MaxWidth(720).AlignSelf(ui.Center).Children(func() {
		settingsCard(c, appui.IconDownload, a.tr("setup.title"), a.tr("setup.desc"), func() {
			if len(a.toolChecks) == 0 {
				ui.Text(c, a.tr("setup.checking")).FontSize(11).TextColor(c.Theme().TextMuted)
				return
			}
			for _, check := range a.toolChecks {
				a.setupToolRow(c, check)
			}
			a.setupActions(c)
		})
	})
}

func (a *App) setupToolRow(c *ui.Context, check tools.Check) {
	t := c.Theme()
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).Children(func() {
		ui.Text(c, check.Name).FontSize(13).Bold().Width(70)
		ui.Column(c).Grow(1).Gap(1).Children(func() {
			version := check.Version
			if version == "" {
				version = "—"
			}
			label := version
			if source := a.toolSourceLabel(check.Source); source != "" {
				label = a.tr("settings.tools.versionSource", map[string]any{"version": version, "source": source})
			}
			ui.Text(c, label).FontSize(12).TextColor(t.TextMuted).Ellipsis("…")
			if check.Path != "" {
				ui.Text(c, check.Path).FontSize(11).TextColor(t.TextMuted).Ellipsis("…")
			}
		})
		status, color := a.toolStatusLabel(check, t)
		ui.Row(c).Background(t.SurfaceHover).Border(1, t.Border).Radius(999).Padding(4, 10).Children(func() {
			ui.Text(c, status).FontSize(11).Bold().TextColor(color)
		})
		if !a.toolInstallRunning {
			if ui.Button(c, a.tr("setup.choose")).Clicked() {
				tool := check.Name
				if tool == "ffprobe" {
					tool = "ffmpeg"
				}
				go a.pickToolPath(tool)
			}
		}
	})
}

func (a *App) toolInstallStatusRow(c *ui.Context) bool {
	t := c.Theme()
	if !a.toolInstallRunning {
		if a.toolInstallErr != "" {
			ui.Text(c, a.toolInstallErr).FontSize(11).TextColor(t.Danger).MaxWidth(680)
		}
		return false
	}
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Spinner(c)
		ui.Text(c, a.toolInstallStageText()).FontSize(12).TextColor(t.TextMuted).Grow(1)
		if a.toolInstallTotal > 0 {
			ui.Progress(c, float64(a.toolInstallDownloaded)/float64(a.toolInstallTotal)).Width(140)
		}
		if ui.Button(c, a.tr("setup.cancel")).Clicked() {
			a.cancelToolInstall()
		}
	})
	if a.toolInstallErr != "" {
		ui.Text(c, a.toolInstallErr).FontSize(11).TextColor(t.TextMuted).MaxWidth(680)
	}
	return true
}

func (a *App) setupActions(c *ui.Context) {
	t := c.Theme()
	if a.toolInstallStatusRow(c) {
		return
	}
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		brew := services.HomebrewPath()
		_, staticAvailable := services.StaticToolSource()
		if a.setupFFmpegMissing() && !a.toolInstallSucceeded && (brew != "" || staticAvailable) {
			label := a.tr("setup.download")
			if brew != "" {
				label = a.tr("setup.installBrew")
			}
			if ui.PrimaryButton(c, label).Clicked() {
				a.startToolInstall(false)
			}
		}
		if a.toolInstallErr != "" && brew != "" && staticAvailable && !a.toolInstallSucceeded {
			if ui.Button(c, a.tr("setup.download")).Clicked() {
				a.startToolInstall(true)
			}
		}
		if ui.Button(c, a.tr("settings.tools.updateYtDlp")).Clicked() {
			a.startYtDlpUpdate(a.settingsSnapshot().YtDlpManifestURL)
		}
		if ui.Button(c, a.tr("setup.later")).Clicked() {
			a.setupDismissed = true
			a.invalidate()
		}
		if ui.Button(c, a.tr("settings.tools.recheck")).Clicked() {
			a.refreshToolChecks()
			a.invalidate()
		}
	})
	if a.setupFFmpegMissing() && a.toolInstallSucceeded {
		ui.Text(c, a.tr("settings.tools.checking")).FontSize(11).TextColor(t.TextMuted)
	} else if a.setupFFmpegMissing() {
		switch {
		case services.HomebrewPath() != "":
			ui.Text(c, a.tr("setup.brewDetected")).FontSize(11).TextColor(t.TextMuted)
		default:
			if _, ok := services.StaticToolSource(); ok {
				ui.Text(c, a.tr("setup.staticFallback")).FontSize(11).TextColor(t.TextMuted)
			} else {
				ui.Text(c, a.tr("setup.installUnavailable")).FontSize(11).TextColor(t.Danger)
			}
		}
	}
}

func (a *App) setupFFmpegMissing() bool {
	for _, check := range a.toolChecks {
		if (check.Name == "ffmpeg" || check.Name == "ffprobe") && !check.OK {
			return true
		}
	}
	return false
}

func (a *App) setToolCheck(check tools.Check) {
	if check.Name == "ffmpeg" || check.Name == "ffprobe" {
		a.toolInstallSucceeded = false
	}
	for i := range a.toolChecks {
		if a.toolChecks[i].Name == check.Name {
			a.toolChecks[i] = check
			return
		}
	}
	a.toolChecks = append(a.toolChecks, check)
	order := map[string]int{"yt-dlp": 0, "ffmpeg": 1, "ffprobe": 2}
	sort.SliceStable(a.toolChecks, func(i, j int) bool {
		return order[a.toolChecks[i].Name] < order[a.toolChecks[j].Name]
	})
}

func (a *App) toolSourceLabel(source string) string {
	switch source {
	case tools.SourceBuiltin:
		return a.tr("settings.tools.source.builtin")
	case tools.SourceManaged:
		return a.tr("settings.tools.source.managed")
	case tools.SourceSystem:
		return a.tr("settings.tools.source.system")
	case tools.SourceCustom:
		return a.tr("settings.tools.source.custom")
	}
	return ""
}

func (a *App) toolStatusLabel(check tools.Check, t *ui.Theme) (string, ui.Color) {
	switch {
	case check.OK:
		return a.tr("settings.tools.available"), t.Success
	case check.TimedOut:
		return a.tr("settings.tools.timedOut"), t.Accent
	case check.Path != "":
		return a.tr("settings.tools.unavailable"), t.Danger
	default:
		return a.tr("settings.tools.missing"), t.Danger
	}
}

func (a *App) toolInstallStageText() string {
	name := a.toolInstallName
	switch a.toolInstallStage {
	case services.ToolStageChecking:
		return a.tr("setup.stage.checking")
	case services.ToolStageDownloading:
		return a.tr("setup.stage.downloading", map[string]any{"name": name})
	case services.ToolStageVerifying:
		return a.tr("setup.stage.verifying", map[string]any{"name": name})
	case services.ToolStageInstalling:
		return a.tr("setup.stage.installing")
	case services.ToolStageSelfCheck:
		return a.tr("setup.stage.selfCheck", map[string]any{"name": name})
	case services.ToolStageHomebrew:
		return a.tr("setup.stage.homebrew")
	default:
		return a.tr("setup.stage.preparing")
	}
}

func (a *App) toolStageLabel(stage services.ToolInstallStage) string {
	switch stage {
	case services.ToolStageChecking:
		return a.tr("setup.stageLabel.checking")
	case services.ToolStageDownloading:
		return a.tr("setup.stageLabel.downloading")
	case services.ToolStageVerifying:
		return a.tr("setup.stageLabel.verifying")
	case services.ToolStageInstalling:
		return a.tr("setup.stageLabel.installing")
	case services.ToolStageSelfCheck:
		return a.tr("setup.stageLabel.selfCheck")
	case services.ToolStageHomebrew:
		return a.tr("setup.stageLabel.homebrew")
	default:
		return a.tr("setup.stageLabel.preparing")
	}
}

func (a *App) toolInstallErrorText(err error) string {
	var installErr *services.ToolInstallError
	if errors.As(err, &installErr) {
		return a.tr("setup.failedStage", map[string]any{
			"stage": a.toolStageLabel(installErr.Stage),
			"err":   installErr.Err.Error(),
		})
	}
	if errors.Is(err, services.ErrToolInstallUnsupported) {
		return a.tr("setup.installUnavailable")
	}
	return a.tr("setup.failed", map[string]any{"err": err.Error()})
}

func (a *App) startToolInstall(useStatic bool) {
	if a.toolInstallRunning || a.toolInstallSucceeded {
		return
	}
	brew := services.HomebrewPath()
	source, staticAvailable := services.StaticToolSource()
	if useStatic {
		brew = ""
	}
	if brew == "" && !staticAvailable {
		a.toolInstallErr = a.tr("setup.installUnavailable")
		a.invalidate()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.toolInstallRunning = true
	a.toolInstallSucceeded = false
	a.toolInstallCancel = cancel
	a.toolInstallStage = services.ToolStagePreparing
	a.toolInstallName = ""
	a.toolInstallDownloaded = 0
	a.toolInstallTotal = 0
	a.toolInstallErr = ""
	a.invalidate()

	go func() {
		defer cancel()
		var lastReport time.Time
		err := services.InstallFFmpeg(ctx, services.ToolInstallOptions{
			ManagedDir: tools.ManagedDir(),
			Homebrew:   brew,
			Source:     source,
		}, func(progress services.ToolInstallProgress) {
			if progress.Stage == services.ToolStageDownloading &&
				!lastReport.IsZero() && time.Since(lastReport) < 200*time.Millisecond &&
				(progress.Total <= 0 || progress.Downloaded < progress.Total) {
				return
			}
			lastReport = time.Now()
			a.updateOnMain(func() {
				a.toolInstallStage = progress.Stage
				a.toolInstallName = progress.Name
				a.toolInstallDownloaded = progress.Downloaded
				a.toolInstallTotal = progress.Total
				a.invalidate()
			})
		})
		a.updateOnMain(func() {
			a.toolInstallRunning = false
			a.toolInstallCancel = nil
			switch {
			case err == nil:
				a.toolInstallErr = ""
				a.toolInstallSucceeded = true
				a.showToast(a.tr("setup.installed"))
				a.refreshToolChecks()
			case errors.Is(err, context.Canceled):
				a.toolInstallErr = a.tr("setup.cancelled")
			default:
				a.toolInstallErr = a.toolInstallErrorText(err)
			}
			a.invalidate()
		})
	}()
}

func (a *App) cancelToolInstall() {
	if a.toolInstallCancel != nil {
		a.toolInstallErr = a.tr("setup.cancelling")
		a.toolInstallCancel()
		a.invalidate()
	}
}

func (a *App) currentYtDlpVersion() string {
	for _, check := range a.toolChecks {
		if check.Name == "yt-dlp" {
			return check.Version
		}
	}
	return ""
}

func (a *App) ytDlpUpdateErrorText(err error) string {
	switch {
	case errors.Is(err, services.ErrYtDlpManifestSignature), errors.Is(err, services.ErrYtDlpManifestFormat):
		return a.tr("setup.manifestInvalid")
	case errors.Is(err, services.ErrYtDlpManifestUnavailable):
		return a.tr("setup.manifestUnavailable")
	default:
		return a.toolInstallErrorText(err)
	}
}

func (a *App) startYtDlpUpdate(manifestURL string) {
	if a.toolInstallRunning {
		return
	}
	currentVersion := a.currentYtDlpVersion()
	ctx, cancel := context.WithCancel(context.Background())
	a.toolInstallRunning = true
	a.toolInstallCancel = cancel
	a.toolInstallStage = services.ToolStageChecking
	a.toolInstallName = "yt-dlp"
	a.toolInstallDownloaded = 0
	a.toolInstallTotal = 0
	a.toolInstallErr = ""
	a.invalidate()

	go func() {
		defer cancel()
		finish := func(err error, version string) {
			a.updateOnMain(func() {
				a.toolInstallRunning = false
				a.toolInstallCancel = nil
				switch {
				case err == nil:
					a.toolInstallErr = ""
					if version != "" {
						a.showToast(a.tr("setup.ytdlpUpdated", map[string]any{"version": version}))
					} else {
						a.showToast(a.tr("setup.ytdlpUpToDate", map[string]any{"version": currentVersion}))
					}
					if version != "" {
						a.refreshToolChecks()
					}
				case errors.Is(err, context.Canceled):
					a.toolInstallErr = a.tr("setup.cancelled")
				default:
					a.toolInstallErr = a.ytDlpUpdateErrorText(err)
				}
				a.invalidate()
			})
		}

		update, err := services.CheckYtDlpUpdate(ctx, manifestURL, services.YtDlpManifestPublicKey())
		if err != nil {
			finish(err, "")
			return
		}
		if currentVersion != "" && services.CompareYtDlpVersions(update.Version, currentVersion) <= 0 {
			finish(nil, "")
			return
		}

		var lastReport time.Time
		err = services.InstallYtDlpUpdate(ctx, update, services.YtDlpInstallOptions{ManagedDir: tools.ManagedDir()}, func(progress services.ToolInstallProgress) {
			if progress.Stage == services.ToolStageDownloading &&
				!lastReport.IsZero() && time.Since(lastReport) < 200*time.Millisecond &&
				(progress.Total <= 0 || progress.Downloaded < progress.Total) {
				return
			}
			lastReport = time.Now()
			a.updateOnMain(func() {
				a.toolInstallStage = progress.Stage
				a.toolInstallName = progress.Name
				a.toolInstallDownloaded = progress.Downloaded
				a.toolInstallTotal = progress.Total
				a.invalidate()
			})
		})
		if err != nil {
			finish(err, "")
			return
		}
		finish(nil, update.Version)
	}()
}

func (a *App) pickToolPath(tool string) {
	title := a.tr("setup.chooseTitleYtDlp")
	if tool == "ffmpeg" {
		title = a.tr("setup.chooseTitleFFmpeg")
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.window(), Title: title})
	if err != nil || len(paths) == 0 {
		return
	}
	path := paths[0]
	a.updateOnMain(func() {
		if tool == "ffmpeg" {
			if !strings.HasPrefix(strings.ToLower(filepath.Base(path)), "ffmpeg") {
				a.showToast(a.tr("setup.chooseFFmpegInvalid"))
				return
			}
			ffprobeName := "ffprobe"
			if runtime.GOOS == "windows" {
				ffprobeName = "ffprobe.exe"
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), ffprobeName)); err != nil {
				a.showToast(a.tr("setup.needsFFprobe"))
				return
			}
		}
		settings := a.settingsSnapshot()
		if tool == "yt-dlp" {
			settings.YtDlpPath = path
		} else {
			settings.FFmpegPath = path
		}
		if a.settingsRepo != nil {
			if err := a.settingsRepo.Save(settings); err != nil {
				a.showToast(a.tr("Could not save settings: ") + err.Error())
				return
			}
		}
		a.applySettings(settings)
		a.refreshToolChecks()
		a.invalidate()
	})
}

// toolReady uses the latest probe result; before the first probe completes it
// falls back to the fast startup path.
func (a *App) toolReady(name string) bool {
	for _, check := range a.toolChecks {
		if check.Name == name {
			// A timed-out probe is not proof that the binary is missing; the
			// resolver keeps using it too. The card still shows the timeout.
			return check.OK || check.TimedOut
		}
	}
	if a.ffmpeg == nil {
		return name == "yt-dlp" && a.ytdlp != nil
	}
	switch name {
	case "yt-dlp":
		return a.ytdlp != nil
	case "ffmpeg":
		return a.ffmpeg.Available()
	case "ffprobe":
		return a.ffmpeg.HasFFprobe()
	}
	return false
}

// requireTools blocks an action before it reaches yt-dlp or FFmpeg and opens
// the same setup card on Home. needProbe is set for conversion, which uses
// ffprobe for duration and codec checks.
func (a *App) requireTools(needYtDlp, needProbe bool) bool {
	if (!needYtDlp || a.toolReady("yt-dlp")) && a.toolReady("ffmpeg") && (!needProbe || a.toolReady("ffprobe")) {
		return true
	}
	a.setupDismissed = false
	a.page = PageDashboard
	a.invalidate()
	a.showToast(a.tr("setup.blocked"))
	return false
}
