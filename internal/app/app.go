package app

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"github.com/fazi-gondal/video-downloader-mygo/internal/config"
	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
	"github.com/fazi-gondal/video-downloader-mygo/internal/services"
	"github.com/fazi-gondal/video-downloader-mygo/internal/tools"
	appui "github.com/fazi-gondal/video-downloader-mygo/internal/ui"
)

//go:embed assets/icon.png
var _trayIconPNG []byte

const (
	PageDashboard = "dashboard"
	PageDownloads = "downloads"
	PageConverter = "converter"
	PageHistory   = "history"
	PageSettings  = "settings"
	PageAbout     = "about"
	PageConfig    = "config"

	audioTracksNone   = "none"
	audioTracksAll    = "all"
	audioTracksCustom = "custom"

	subtitleModeNone    = "none"
	subtitleModeAll     = "all"
	subtitleModeCustom  = "custom"
	thumbnailCacheLimit = 256

	sidebarWidth    = 212
	pageInset       = 16
	heroInset       = 40
	topBarHeight    = 44
	contentMaxWidth = 720
	contentMinWidth = 320
)

type App struct {
	winMu                     sync.RWMutex
	win                       *mygo.Window
	pendingMu                 sync.Mutex
	pendingUpdates            []func()
	settingsRepo              *config.SettingsRepository
	settingsMu                sync.RWMutex
	settings                  config.AppSettings // worker-visible snapshot, protected by settingsMu
	settingsDraft             config.AppSettings // editable UI state, accessed on the main thread
	settingsDraftReady        bool
	settingsSlidersReady      bool
	maxConcurrentSlider       float64
	concurrentFragmentsSlider float64
	ffmpeg                    *services.FFmpegService
	ytdlp                     *services.YtDlpService
	manager                   *services.DownloadManager
	conv                      *services.ConversionService
	history                   *services.HistoryService
	resumeStore               *services.ResumeStore
	closePrompting            atomic.Bool
	closeApproved             atomic.Bool
	trayMu                    sync.Mutex
	tray                      *mygo.Tray

	page string

	urlInput           string
	analyzing          bool
	analyzeError       string
	analyzeUnsupported bool
	currentMedia       *models.MediaInfo
	currentPlaylist    *models.PlaylistInfo
	selectedEntries    map[int]bool

	extractOpen           bool
	extractProgress       float32
	heroContentHeight     float32
	extractInput          string
	extractBusy           bool
	extractError          string
	extractResult         *services.SiteExtractResult
	extractItems          []extractItem
	extractSelected       map[int]bool
	extractToken          uint64
	extractCancel         context.CancelFunc
	toolChecks            []tools.Check
	toolCheckToken        uint64
	appliedYtDlpPath      string
	appliedFFmpegPath     string
	setupDismissed        bool
	toolInstallRunning    bool
	toolInstallCancel     context.CancelFunc
	toolInstallStage      services.ToolInstallStage
	toolInstallName       string
	toolInstallDownloaded int64
	toolInstallTotal      int64
	toolInstallErr        string
	toolInstallSucceeded  bool
	lang                  string

	mode                models.DownloadMode
	maxHeight           int
	container           string
	preferredContainer  string
	audioFormat         string
	audioTracksMode     string
	selectedAudio       map[string]*bool
	subtitleMode        string
	selectedSubtitle    map[string]*bool
	selectedVideoFormat string
	selectedAudioFormat string
	selectedVideoLabel  string // shown in Select
	selectedAudioLabel  string
	formatsOpen         bool // Collapsible: advanced format IDs
	formatsLoading      bool

	videoFmtLabels []string
	videoFmtIDs    []string
	audioFmtLabels []string
	audioFmtIDs    []string

	playlistQueued, playlistDone, playlistFailed int
	playlistActive                               bool
	playlistBatchID                              uint64

	tasksMu sync.Mutex
	tasks   []models.DownloadTaskSnapshot

	convSource string
	convFormat string
	convKind   models.MediaKind
	// convAudioBitrateKbps is used for lossy audio conversion targets.
	convAudioBitrateKbps int
	convJobs             []models.ConversionJobSnapshot
	convJobsMu           sync.Mutex
	remuxMu              sync.Mutex
	remuxSource          string
	remuxTarget          string
	remuxChecking        bool
	remuxKnown           bool
	remuxResult          bool

	historyEntries []services.HistoryEntry
	historyLoaded  bool
	historyQuery   string
	historyFilter  string

	settingsHeaders       string
	settingsSubtitleLangs string
	settingsRateLimit     string

	dlListState, plListState, fmtListState, histListState, convListState, audListState, subListState ui.ListState

	toastMsg    string
	toastUntil  time.Time
	sidebarOpen [2]bool

	thumbnailMu       sync.Mutex
	thumbnails        map[string]*ui.Bitmap
	thumbnailLoading  map[string]bool
	thumbnailFailures map[string]bool
	thumbnailOrder    []string
}

func cloneAppSettings(settings config.AppSettings) config.AppSettings {
	if settings.CustomHeaders != nil {
		headers := settings.CustomHeaders
		settings.CustomHeaders = make(map[string]string, len(headers))
		for key, value := range headers {
			settings.CustomHeaders[key] = value
		}
	}
	if settings.SubtitleLangs != nil {
		settings.SubtitleLangs = append([]string(nil), settings.SubtitleLangs...)
	}
	return settings
}

// settingsSnapshot returns an immutable copy for worker and background code.
func (a *App) settingsSnapshot() config.AppSettings {
	a.settingsMu.RLock()
	defer a.settingsMu.RUnlock()
	return cloneAppSettings(a.settings)
}

func (a *App) setSettings(settings config.AppSettings) {
	settings = cloneAppSettings(settings)
	a.settingsMu.Lock()
	a.settings = settings
	a.settingsMu.Unlock()
}

func (a *App) ensureSettingsDraft() {
	if a.settingsDraftReady {
		if !a.settingsSlidersReady {
			a.syncPerformanceSliders(a.settingsDraft)
		}
		return
	}
	a.settingsDraft = a.settingsSnapshot()
	a.settingsDraftReady = true
	a.syncPerformanceSliders(a.settingsDraft)
}

func (a *App) settingsDraftSnapshot() config.AppSettings {
	a.ensureSettingsDraft()
	return cloneAppSettings(a.settingsDraft)
}

func (a *App) publishSettingsDraft() {
	a.ensureSettingsDraft()
	a.setSettings(a.settingsDraft)
}

func (a *App) applySettings(settings config.AppSettings) (toolPathsChanged bool) {
	settings = cloneAppSettings(settings)
	toolPathsChanged = a.appliedYtDlpPath != settings.YtDlpPath || a.appliedFFmpegPath != settings.FFmpegPath
	tools.SetOverrides(settings.YtDlpPath, settings.FFmpegPath)
	services.SetAryPlusAPIKey(settings.ARYPlusAPIKey)
	a.appliedYtDlpPath = settings.YtDlpPath
	a.appliedFFmpegPath = settings.FFmpegPath
	a.setSettings(settings)
	a.settingsDraft = cloneAppSettings(settings)
	a.settingsDraftReady = true
	a.syncPerformanceSliders(settings)
	return toolPathsChanged
}

func (a *App) syncPerformanceSliders(settings config.AppSettings) {
	a.maxConcurrentSlider = float64(min(max(settings.MaxConcurrent, 1), 16))
	a.concurrentFragmentsSlider = float64(min(max(settings.ConcurrentFragments, 1), 32))
	a.settingsSlidersReady = true
}

func New() *App {
	repo := config.NewSettingsRepository()
	settings, err := repo.Load()
	if err != nil {
		log.Println("settings:", err)
	}
	tools.SetOverrides(settings.YtDlpPath, settings.FFmpegPath)
	tools.RecoverManagedTools(time.Now())
	services.SetAryPlusAPIKey(settings.ARYPlusAPIKey)
	ffmpeg := services.NewFFmpegService()
	a := &App{
		settingsRepo:         repo,
		settings:             cloneAppSettings(settings),
		settingsDraft:        cloneAppSettings(settings),
		settingsDraftReady:   true,
		appliedYtDlpPath:     settings.YtDlpPath,
		appliedFFmpegPath:    settings.FFmpegPath,
		ffmpeg:               ffmpeg,
		page:                 PageDashboard,
		mode:                 models.ModeVideoAudio,
		maxHeight:            1080,
		container:            "mp4",
		preferredContainer:   "mp4",
		audioFormat:          "mp3",
		audioTracksMode:      audioTracksNone,
		subtitleMode:         subtitleModeNone,
		convFormat:           "mp3",
		convKind:             models.KindAudio,
		convAudioBitrateKbps: 320,
		selectedEntries:      map[int]bool{},
		selectedAudio:        map[string]*bool{},
		selectedSubtitle:     map[string]*bool{},
		sidebarOpen:          [2]bool{true, true},
		formatsOpen:          false,
		selectedVideoLabel:   "Auto (best for resolution)",
		selectedAudioLabel:   "Auto (Opus / best)",
		historyFilter:        "all",
		settingsRateLimit:    strconv.Itoa(settings.RateLimitKbps),
		thumbnails:           map[string]*ui.Bitmap{},
		thumbnailLoading:     map[string]bool{},
		thumbnailFailures:    map[string]bool{},
	}
	a.syncPerformanceSliders(settings)
	var headerLines []string
	for key, value := range settings.CustomHeaders {
		headerLines = append(headerLines, key+": "+value)
	}
	sort.Strings(headerLines)
	a.settingsHeaders = strings.Join(headerLines, "\n")
	a.settingsSubtitleLangs = strings.Join(settings.SubtitleLangs, ", ")
	if settings.MultiAudio {
		a.audioTracksMode = audioTracksAll
	}
	if settings.WriteSubtitles {
		a.subtitleMode = subtitleModeFromSettings(settings)
		for _, lang := range settings.SubtitleLangs {
			if lang != "" && lang != "all" {
				a.selectedSubtitle[lang] = boolPtr(true)
			}
		}
	}
	a.ytdlp = services.NewYtDlpService(ffmpeg.Path(), a.settingsSnapshot)
	a.manager = services.NewDownloadManager(a.ytdlp, settings.MaxConcurrent)
	a.conv = services.NewConversionService(ffmpeg)
	hist, err := services.NewHistoryService()
	if err != nil {
		log.Println("history:", err)
	}
	a.history = hist
	resumeStore, err := services.NewResumeStore()
	if err != nil {
		log.Println("download recovery:", err)
		a.toastMsg = "Download recovery is unavailable: " + err.Error()
		a.toastUntil = time.Now().Add(5 * time.Second)
	} else {
		a.resumeStore = resumeStore
		if err := a.restoreResumableDownloads(); err != nil {
			log.Println("download recovery:", err)
			a.toastMsg = "Could not restore paused downloads: " + err.Error()
			a.toastUntil = time.Now().Add(5 * time.Second)
		}
	}
	if a.resumeStore != nil {
		go a.cleanupStaleStagingDirs()
	}
	a.refreshToolChecks()
	go a.pumpDownloadEvents()
	go a.pumpConversionEvents()
	return a
}

func (a *App) SetWindow(w *mygo.Window) {
	w.OnClose(a.handleWindowClose)
	a.winMu.Lock()
	a.win = w
	a.pendingMu.Lock()
	pending := append([]func(){}, a.pendingUpdates...)
	a.pendingUpdates = nil
	a.pendingMu.Unlock()
	a.winMu.Unlock()
	for _, fn := range pending {
		w.Update(fn)
	}
}

func (a *App) restoreResumableDownloads() error {
	snapshots, err := a.resumeStore.Load()
	if err != nil {
		return err
	}
	for _, snapshot := range snapshots {
		if snapshot.ID == "" || snapshot.Request.URL == "" {
			log.Printf("download recovery: skipping invalid task record %q", snapshot.ID)
			continue
		}
		task := models.RestorePausedDownloadTask(snapshot)
		if err := a.manager.RestorePausedTask(task); err != nil {
			log.Printf("download recovery: could not restore task %q: %v", snapshot.ID, err)
		}
	}
	a.tasksMu.Lock()
	a.tasks = a.manager.Snapshots()
	a.tasksMu.Unlock()
	return nil
}

// cleanupStaleStagingDirs removes old .mygo-download-* directories left by a
// crash. It only scans known output directories, never follows symlinks, and
// keeps every staging directory recorded by a paused task. Errors are logged;
// startup and downloads are never blocked.
func (a *App) cleanupStaleStagingDirs() {
	if a.resumeStore == nil {
		return
	}
	snapshots, err := a.resumeStore.Load()
	if err != nil {
		log.Printf("staging cleanup: cannot read paused downloads: %v", err)
		return
	}

	dirs := []string{a.settingsSnapshot().DownloadDir}
	keep := make(map[string]bool)
	addSnapshot := func(snapshot models.DownloadTaskSnapshot) {
		if snapshot.StagingDir != "" {
			keep[snapshot.StagingDir] = true
		}
		if snapshot.Request.OutputDir != "" {
			dirs = append(dirs, snapshot.Request.OutputDir)
		}
	}
	for _, snapshot := range snapshots {
		addSnapshot(snapshot)
	}
	if a.manager != nil {
		for _, snapshot := range a.manager.Snapshots() {
			addSnapshot(snapshot)
		}
	}
	if a.history != nil {
		entries, err := a.history.List(500)
		if err != nil {
			log.Printf("staging cleanup: read history: %v", err)
		}
		for _, entry := range entries {
			if entry.OutputDir != "" {
				dirs = append(dirs, entry.OutputDir)
			}
		}
	}

	result := services.CleanupStaleStagingDirs(dirs, keep, services.DefaultStaleStagingAge, time.Now())
	for _, path := range result.Removed {
		log.Printf("staging cleanup: removed %s", path)
	}
	for _, err := range result.Errors {
		log.Printf("staging cleanup: %v", err)
	}
}

func (a *App) persistResumableDownloads() error {
	if a.manager == nil {
		return nil
	}
	if a.resumeStore == nil {
		return fmt.Errorf("download recovery store is unavailable")
	}
	snapshots := a.manager.Snapshots()
	resumable := snapshots[:0]
	for _, snapshot := range snapshots {
		if !snapshot.State.IsTerminal() {
			resumable = append(resumable, snapshot)
		}
	}
	return a.resumeStore.Save(resumable)
}

func (a *App) handleWindowClose(e *mygo.CloseEvent) {
	if a.closeApproved.Load() || a.manager == nil || !a.hasUnfinishedDownloads() {
		return
	}
	e.PreventDefault()
	if !a.closePrompting.CompareAndSwap(false, true) {
		return
	}
	go a.promptCloseWithDownloads()
}

func (a *App) hasUnfinishedDownloads() bool {
	for _, snapshot := range a.manager.Snapshots() {
		if !snapshot.State.IsTerminal() {
			return true
		}
	}
	return false
}

func (a *App) promptCloseWithDownloads() {
	result, err := mygo.Dialog.Message(mygo.MessageOptions{
		Parent:        a.window(),
		Type:          mygo.MessageQuestion,
		Title:         "Downloads need attention",
		Message:       "Some downloads are active or paused. What would you like to do?",
		Detail:        "\"Minimize to tray\" keeps downloads running in the background. Paused downloads can be resumed the next time you open the app.",
		Buttons:       []string{"Keep downloading", "Minimize to tray", "Pause and close", "Cancel downloads and close"},
		DefaultButton: 1,
		CancelButton:  0,
	})
	if err != nil {
		log.Println("close confirmation:", err)
		a.closePrompting.Store(false)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch result.Button {
	case 1:
		// Minimize to tray: hide the window and keep downloads running.
		a.minimizeToTray()
		return // do NOT set closeApproved; window is just hidden
	case 2:
		if err := a.manager.PauseAll(ctx); err != nil {
			mygo.Dialog.Error("Could not pause downloads", err.Error())
			a.closePrompting.Store(false)
			return
		}
		if err := a.persistResumableDownloads(); err != nil {
			mygo.Dialog.Error("Could not save paused downloads", err.Error())
			a.closePrompting.Store(false)
			return
		}
	case 3:
		a.manager.CancelAll()
		if err := a.manager.WaitForTerminal(ctx); err != nil {
			mygo.Dialog.Error("Could not stop downloads", err.Error())
			a.closePrompting.Store(false)
			return
		}
		if a.resumeStore != nil {
			if err := a.resumeStore.Save(nil); err != nil {
				mygo.Dialog.Error("Could not clear download recovery data", err.Error())
				a.closePrompting.Store(false)
				return
			}
		}
	default:
		a.closePrompting.Store(false)
		return
	}

	a.closeApproved.Store(true)
	if win := a.window(); win != nil {
		win.Close()
	}
}

// minimizeToTray hides the window and shows a system-tray icon so downloads
// continue running in the background. The user can restore the window by
// clicking the tray icon, or quit cleanly from the tray menu.
func (a *App) minimizeToTray() {
	win := a.window()
	if win == nil {
		a.closePrompting.Store(false)
		return
	}

	trayMenu := mygo.NewMenu([]*mygo.MenuItem{
		{
			Label: "Show Video Downloader",
			Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.showFromTray() },
		},
		mygo.Separator(),
		{
			Label: "Quit",
			Click: func(_ *mygo.MenuItem, _ *mygo.Window) { a.quitFromTray() },
		},
	})

	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:      "vd-main",
		Icon:    _trayIconPNG,
		ToolTip: "Video Downloader — downloading in background",
		Menu:    trayMenu,
	})
	if err != nil {
		// Tray unavailable (e.g. Linux without libayatana); fall back to keeping the window open.
		log.Println("tray: could not create tray icon:", err)
		a.closePrompting.Store(false)
		return
	}
	tray.OnClick(func() { a.showFromTray() })

	a.trayMu.Lock()
	a.tray = tray
	a.trayMu.Unlock()

	win.SetSkipTaskbar(true) // remove from Windows taskbar while tray-only
	win.Hide()
	a.closePrompting.Store(false)
}

// showFromTray restores the main window and removes the tray icon.
func (a *App) showFromTray() {
	a.trayMu.Lock()
	t := a.tray
	a.tray = nil
	a.trayMu.Unlock()
	if t != nil {
		t.Destroy()
	}
	win := a.window()
	if win == nil {
		return
	}
	win.SetSkipTaskbar(false)
	win.Show()
	win.Focus()
}

// quitFromTray performs a clean shutdown initiated from the tray menu.
func (a *App) quitFromTray() {
	a.closeApproved.Store(true)
	win := a.window()
	if win == nil {
		mygo.App.Quit()
		return
	}
	win.Show() // must be visible for Close to fire the lifecycle hooks
	win.Close()
}

// ShowMainWindow brings the existing instance to the front when a second
// launch is handed over to this process.
func (a *App) ShowMainWindow() {
	if a.HasTray() {
		a.showFromTray()
		return
	}
	win := a.window()
	if win == nil {
		return
	}
	win.Show()
	if win.IsMinimized() {
		win.Restore()
	}
	win.Focus()
}

// HasTray reports whether a tray icon is currently shown (i.e. the app is
// running in the background). Used by main.go to guard OnWindowAllClosed.
func (a *App) HasTray() bool {
	a.trayMu.Lock()
	defer a.trayMu.Unlock()
	return a.tray != nil
}

// Shutdown stops media workers before the application event loop exits. Both
// services make this operation idempotent so it is safe to call from the
// window lifecycle hook and from main's deferred cleanup.
func (a *App) Shutdown() {
	if a.toolInstallCancel != nil {
		a.toolInstallCancel()
	}
	if a.manager != nil {
		a.manager.Shutdown()
	}
	if a.conv != nil {
		a.conv.Shutdown()
	}
}

func (a *App) window() *mygo.Window {
	a.winMu.RLock()
	defer a.winMu.RUnlock()
	return a.win
}

func (a *App) updateOnMain(fn func()) {
	a.winMu.Lock()
	if a.win != nil {
		win := a.win
		a.winMu.Unlock()
		win.Update(fn)
		return
	}
	a.pendingMu.Lock()
	a.pendingUpdates = append(a.pendingUpdates, fn)
	a.pendingMu.Unlock()
	a.winMu.Unlock()
}

func (a *App) invalidate() {
	if win := a.window(); win != nil {
		win.Invalidate()
	}
}
func (a *App) showToast(msg string) {
	a.toastMsg = msg
	a.toastUntil = time.Now().Add(3 * time.Second)
	a.invalidate()
}

func (a *App) recordPlaylistTask(task models.DownloadTaskSnapshot) bool {
	if !a.playlistActive || task.Request.PlaylistBatchID == 0 || task.Request.PlaylistBatchID != a.playlistBatchID {
		return false
	}
	if task.State == models.StateCompleted {
		a.playlistDone++
	} else {
		a.playlistFailed++
	}
	if a.playlistDone+a.playlistFailed >= a.playlistQueued {
		a.playlistActive = false
		return true
	}
	return false
}

func (a *App) pumpDownloadEvents() {
	for ev := range a.manager.Events() {
		if ev.Task == nil {
			continue
		}
		task := ev.Task.Snapshot()
		if ev.Kind == services.EventTaskQueued || ev.Kind == services.EventTaskState || ev.Kind == services.EventTaskFinished {
			if err := a.persistResumableDownloads(); err != nil {
				log.Println("download recovery:", err)
				a.updateOnMain(func() {
					a.showToast(a.tr("Could not save download recovery data"))
				})
			}
		}
		if ev.Kind == services.EventTaskFinished && a.history != nil {
			if err := a.history.Record(ev.Task); err != nil {
				log.Println("history:", err)
			}
		}
		a.updateOnMain(func() {
			a.tasksMu.Lock()
			a.tasks = a.manager.Snapshots()
			a.tasksMu.Unlock()
			if ev.Kind == services.EventTaskFinished {
				if a.history != nil {
					a.historyLoaded = false
				}
				if a.recordPlaylistTask(task) {
					a.showToast(a.tr("downloads.playlistDone", map[string]any{"ok": a.playlistDone, "failed": a.playlistFailed}))
				}
				if task.State == models.StateCancelled {
					a.showToast(a.tr("Download cancelled"))
				} else if ev.Error == nil {
					name := filepath.Base(task.OutputPath)
					if name == "." || name == "" {
						name = task.Request.Title
					}
					a.showToast(a.tr("Downloaded: ") + name)
				} else if !a.playlistActive {
					a.showToast(a.tr("Download failed"))
				}
			}
			a.invalidate()
		})
	}
}

func (a *App) pumpConversionEvents() {
	for ev := range a.conv.Events() {
		if ev.Job == nil {
			continue
		}
		job := ev.Job.Snapshot()
		a.updateOnMain(func() {
			a.convJobsMu.Lock()
			a.convJobs = a.conv.Snapshots()
			a.convJobsMu.Unlock()
			if ev.Kind == services.ConvEventFinished {
				if job.State == models.ConvCancelled {
					a.showToast(a.tr("Conversion cancelled"))
				} else if ev.Err == nil {
					a.showToast(a.tr("Converted: ") + filepath.Base(job.OutputPath))
				} else {
					a.showToast(a.tr("Conversion failed"))
				}
			}
			a.invalidate()
		})
	}
}

func (a *App) View(c *ui.Context) {
	a.ensureSettingsDraft()
	if locale := a.preferredLocale(); locale != a.lang {
		a.lang = locale
		mygo.App.SetLocale(locale)
	}
	defer a.publishSettingsDraft()
	// Apply the reference app's palette without replacing mygo's native
	// controls. The initial theme tells us whether the OS is currently dark.
	dark := c.Theme().Dark
	switch a.settingsDraft.ThemeMode {
	case "dark":
		dark = true
	case "light":
		dark = false
	}
	c.SetTheme(appui.NativeTheme(dark))
	t := c.Theme()
	a.tasksMu.Lock()
	activeDownloads, failedDownloads := 0, 0
	for _, task := range a.tasks {
		if task.State == models.StateError {
			failedDownloads++
		}
		if !task.State.IsTerminal() && task.State != models.StatePaused {
			activeDownloads++
		}
	}
	a.tasksMu.Unlock()
	if !c.FocusTakesText() {
		shortcutMod := ui.Super
		if runtime.GOOS != "darwin" {
			shortcutMod = ui.Ctrl
		}
		switch {
		case c.Shortcut(shortcutMod, ui.Key1):
			a.page = PageDashboard
		case c.Shortcut(shortcutMod, ui.Key2):
			a.page = PageDownloads
		case c.Shortcut(shortcutMod, ui.Key3):
			a.page = PageHistory
		case c.Shortcut(shortcutMod, ui.Key4):
			a.page = PageConverter
		case c.Shortcut(shortcutMod, ui.KeyComma):
			a.page = PageSettings
		}
	}
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		// Left nav: sidebar sections (scrollable) + theme toggle fixed at bottom.
		// The Column wrapper is necessary because ui.Sidebar is a scroll container —
		// a Spacer inside it would scroll away. Wrapping ensures the toggle stays
		// anchored below the scroll regardless of content height.
		ui.Column(c).Width(sidebarWidth).FillHeight().Material(glass.Glass{Clarity: 0.2}).Children(func() {
			ui.Sidebar(c, &a.page, func() {
				ui.SidebarSection(c, a.tr("nav.library"), &a.sidebarOpen[0], func() {
					ui.SidebarItem(c, PageDashboard, appui.IconHome, a.tr("nav.home"))
					ui.SidebarItem(c, PageDownloads, appui.IconDownload, a.tr("nav.downloads")).Children(func() {
						if failedDownloads > 0 {
							ui.Badge(c, "!")
						} else if activeDownloads > 0 {
							ui.Badge(c, fmt.Sprintf("%d", activeDownloads))
						}
					})
					ui.SidebarItem(c, PageHistory, appui.IconHistory, a.tr("nav.history"))
					ui.SidebarItem(c, PageConverter, appui.IconConverter, a.tr("nav.converter"))
				})
				ui.SidebarSection(c, a.tr("nav.app"), &a.sidebarOpen[1], func() {
					ui.SidebarItem(c, PageSettings, appui.IconSettings, a.tr("nav.settings"))
					ui.SidebarItem(c, PageAbout, appui.IconAbout, a.tr("nav.about"))
				})
			}).Grow(1)
		})
		ui.Column(c).Grow(1).FillHeight().Background(t.Background).Children(func() {
			a.topBar(c)
			ui.Column(c).Grow(1).Padding(16).Children(func() {
				switch a.page {
				case PageDashboard:
					a.dashboardPage(c)
				case PageConfig:
					a.configPage(c)
				case PageDownloads:
					a.downloadsPage(c)
				case PageConverter:
					a.converterPage(c)
				case PageHistory:
					a.historyPage(c)
				case PageSettings:
					a.refinedSettingsPage(c)
				case PageAbout:
					a.refinedAboutPage(c)
				}
			})
			a.toastBar(c)
		})
	})
}

func (a *App) topBar(c *ui.Context) {
	t := c.Theme()
	titles := map[string]string{
		PageDashboard: a.tr("nav.home"), PageDownloads: a.tr("nav.downloads"), PageConverter: a.tr("nav.converter"),
		PageHistory: a.tr("nav.history"), PageSettings: a.tr("nav.settings"), PageAbout: a.tr("nav.about"),
		PageConfig: a.tr("nav.downloadOptions"),
	}
	ui.Row(c).Height(topBarHeight).FillWidth().Padding(0, 16).AlignItems(ui.Center).Material(glass.Glass{Clarity: 0.2}).Children(func() {
		ui.Text(c, titles[a.page]).FontSize(15).Bold()
		ui.Spacer(c).Grow(1)
		if a.playlistActive {
			done := a.playlistDone + a.playlistFailed
			ui.Text(c, a.tr("downloads.playlistProgress", map[string]any{"done": done, "total": a.playlistQueued})).FontSize(12).TextColor(t.TextMuted)
			ui.Progress(c, float64(done)/float64(maxInt(a.playlistQueued, 1))).Width(120)
		}
		a.tasksMu.Lock()
		n := 0
		for _, task := range a.tasks {
			if !task.State.IsTerminal() && task.State != models.StatePaused {
				n++
			}
		}
		a.tasksMu.Unlock()
		if n > 0 {
			ui.Text(c, a.tr("downloads.activeCount", map[string]any{"n": n})).FontSize(12).TextColor(t.Accent)
		}
		a.themeToggleButton(c, t)
	})
}

// themeToggleButton switches between light and dark from the top bar.
func (a *App) themeToggleButton(c *ui.Context, t *ui.Theme) {
	icon := appui.IconMoon
	next := "dark"
	if t.Dark {
		icon = appui.IconSun
		next = "light"
	}
	button := ui.Row(c.Key("topbar-theme")).
		Size(28, 28).
		Radius(8).
		Center().
		Children(func() {
			ui.Icon(c, icon).FontSize(16).TextColor(t.TextMuted)
		})
	if button.Clicked() {
		a.settingsDraft.ThemeMode = next
		a.invalidate()
	}
}

func (a *App) toastBar(c *ui.Context) {
	if a.toastMsg == "" || time.Now().After(a.toastUntil) {
		return
	}
	t := c.Theme()
	ui.Row(c).FillWidth().Justify(ui.Center).Padding(0, 0, 16, 0).Children(func() {
		ui.Row(c).Material(glass.Glass{Clarity: 0.3}).Border(1, t.Border).Padding(8, 14).Radius(8).Children(func() {
			ui.Text(c, a.toastMsg).FontSize(13)
		})
	})
}

func (a *App) dashboardPage(c *ui.Context) {
	t := c.Theme()
	// ui.Scroll lets media + formats + options scroll when they overflow the window.
	ui.Scroll(c).Grow(1).Gap(12).Children(func() {
		idle := a.currentMedia == nil && a.currentPlaylist == nil && !a.analyzing && a.analyzeError == ""
		if idle {
			a.dashboardHero(c)
			return
		}

		a.dashboardSearch(c)
		a.extractorCollapsible(c, pageContentWidth(c))
		if a.setupVisible() {
			a.setupCard(c)
		}
		if a.analyzeError != "" {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).Background(t.Surface).Border(1, t.Danger).Radius(10).Padding(10, 12).Children(func() {
				ui.Text(c, a.friendlyError(a.analyzeError)).FontSize(13).TextColor(t.Danger).Grow(1)
				if a.analyzeUnsupported {
					if ui.Button(c.Key("dashboard-try-extractor"), a.tr("extractor.try")).Clicked() {
						a.extractInput = strings.TrimSpace(a.urlInput)
						a.extractOpen = true
						a.page = PageDashboard
						a.invalidate()
					}
				}
			})
		}
		if a.currentMedia != nil {
			a.mediaCard(c)
			ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
				if a.isDirectHDMedia() {
					if ui.PrimaryButton(c, a.tr("Download HD")).Clicked() {
						a.startDirectHDDownload()
					}
				} else if ui.PrimaryButton(c, a.tr("Continue to download options")).Clicked() {
					a.page = PageConfig
				}
			})
		} else if a.currentPlaylist != nil {
			a.playlistPanel(c)
		} else if a.analyzing {
			ui.Column(c).FillWidth().Padding(42, 0).Center().Gap(10).Children(func() {
				ui.Spinner(c)
				ui.Text(c, a.tr("Analyzing media…")).FontSize(13).TextColor(t.TextMuted)
			})
		}
	})
}

func (a *App) dashboardHero(c *ui.Context) {
	t := c.Theme()
	_, windowHeight := c.Size()
	viewport := windowHeight - topBarHeight - 2*pageInset

	// The hero's top offset is animated, never derived from the live content
	// height. Otherwise the panel's own expand/collapse and the re-centering
	// of the column read as two separate animations, especially on close.
	targetPad := float32(40)
	if !a.extractOpen {
		height := a.heroContentHeight
		if height <= 0 {
			height = 300
		}
		if centered := (viewport - height) / 2; centered > targetPad {
			targetPad = centered
		}
	}
	ui.Scroll(c).Grow(1).Children(func() {
		column := ui.Column(c)
		pad := column.Animate("hero-pad", targetPad, 200*time.Millisecond)
		column.FillWidth().
			Padding(pad, heroInset, 0, heroInset).
			AlignItems(ui.Center).
			Children(func() {
				content := ui.Column(c).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Column(c).AlignItems(ui.Center).Gap(12).Children(func() {
						ui.Row(c).Justify(ui.Center).Gap(12).Children(func() {
							ui.Text(c, a.tr("Extract.")).FontSize(30).Bold().LetterSpacing(0.5)
							ui.Text(c, a.tr("Convert.")).FontSize(30).Bold().TextColor(t.TextMuted).LetterSpacing(0.5)
							ui.Text(c, a.tr("Master.")).FontSize(30).Bold().TextColor(t.Accent).LetterSpacing(0.5)
						})
						ui.Text(c, a.tr("Download, organize, and convert media from your favorite platforms.")).
							FontSize(14).TextColor(t.TextMuted).MaxWidth(520)
					})

					ui.Column(c).FillWidth().AlignItems(ui.Center).Gap(16).Padding(32, 0, 0, 0).Children(func() {
						a.dashboardSearch(c)
						a.extractorCollapsible(c, heroContentWidth(c))
						if a.setupVisible() {
							ui.Column(c).FillWidth().Padding(8, 0, 0, 0).Children(func() {
								a.setupCard(c)
							})
						}
					})

					ui.Column(c).AlignItems(ui.Center).Gap(10).Padding(36, 0, 0, 0).Children(func() {
						ui.Text(c, a.tr("SUPPORTED SERVICES")).FontSize(11).Bold().TextColor(t.TextMuted)
						ui.Row(c).FillWidth().Justify(ui.Center).Gap(20).Children(func() {
							for i, service := range []string{"YouTube", "TikTok", "Instagram", "X", "ARY Plus"} {
								ui.Row(c).Gap(6).Children(func() {
									icon := appui.IconVideo
									if i == 1 {
										icon = appui.IconMusic
									}
									ui.Icon(c, icon).FontSize(14).TextColor(t.TextMuted)
									ui.Text(c, service).FontSize(12).TextColor(t.TextMuted)
								})
							}
						})
					})
				})
				// Measure only while settled: during the close animation the
				// panel is still in the content and would inflate the height.
				if !a.extractOpen && a.extractProgress == 0 {
					if bounds := content.Bounds(); bounds.H > 0 {
						a.heroContentHeight = bounds.H
					}
				}
			})
	})
}

func (a *App) dashboardSearch(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).FillWidth().MaxWidth(720).AlignSelf(ui.Center).Material(glass.Glass{Clarity: 0.25}).Border(1, t.Border).Radius(12).Padding(8, 10).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, appui.IconLink).FontSize(18).TextColor(t.TextMuted).Width(20)
		ui.TextInput(c, &a.urlInput).Label(a.tr("Video URL")).Placeholder(a.tr("Paste video or playlist URL…")).Grow(1).OnSubmit(a.startAnalyze)
		btn := ui.PrimaryButton(c, a.tr("Analyze"))
		if a.analyzing {
			btn.Disabled(true)
		}
		if btn.Clicked() && !a.analyzing {
			a.startAnalyze()
		}
	})
}

func (a *App) startAnalyze() {
	if a.analyzing {
		return
	}
	a.analyzing = true
	a.analyzeError = ""
	a.analyzeUnsupported = false
	a.resetMediaSelection()
	a.invalidate()
	go a.doAnalyze(strings.TrimSpace(a.urlInput))
}

// resetMediaSelection clears the per-media UI state shared by the normal
// analyze flow and the extractor flow.
func (a *App) resetMediaSelection() {
	a.currentMedia = nil
	a.currentPlaylist = nil
	a.selectedEntries = map[int]bool{}
	a.selectedAudio = map[string]*bool{}
	a.selectedSubtitle = map[string]*bool{}
	a.selectedVideoFormat = ""
	a.selectedAudioFormat = ""
	a.selectedVideoLabel = "Auto (best for resolution)"
	a.selectedAudioLabel = "Auto (Opus / best)"
	a.videoFmtLabels = nil
	a.videoFmtIDs = nil
	a.audioFmtLabels = nil
	a.audioFmtIDs = nil
	a.formatsOpen = false
}

func (a *App) thumbnailElement(c *ui.Context, url string, width, height float32, fallback *ui.SVG) ui.Element {
	if bitmap := a.thumbnail(url); bitmap != nil {
		return ui.Image(c, bitmap).Size(width, height).Fit(ui.Cover).Radius(10)
	}
	a.ensureThumbnail(url)
	return ui.Box(c).Size(width, height).Background(c.Theme().SurfaceHover).Radius(10).Center().Children(func() {
		ui.Icon(c, fallback).FontSize(22).TextColor(c.Theme().TextMuted)
	})
}

func (a *App) thumbnail(url string) *ui.Bitmap {
	if strings.TrimSpace(url) == "" {
		return nil
	}
	a.thumbnailMu.Lock()
	defer a.thumbnailMu.Unlock()
	return a.thumbnails[url]
}

func (a *App) ensureThumbnail(url string) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	a.thumbnailMu.Lock()
	if a.thumbnails == nil {
		a.thumbnails = make(map[string]*ui.Bitmap)
	}
	if a.thumbnailLoading == nil {
		a.thumbnailLoading = make(map[string]bool)
	}
	if a.thumbnailFailures == nil {
		a.thumbnailFailures = make(map[string]bool)
	}
	if a.thumbnails[url] != nil || a.thumbnailLoading[url] || a.thumbnailFailures[url] {
		a.thumbnailMu.Unlock()
		return
	}
	a.thumbnailLoading[url] = true
	a.thumbnailMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		bitmap, err := fetchThumbnail(ctx, nil, url)
		a.thumbnailMu.Lock()
		delete(a.thumbnailLoading, url)
		if err != nil {
			a.thumbnailFailures[url] = true
		} else {
			a.thumbnails[url] = bitmap
			delete(a.thumbnailFailures, url)
		}
		a.rememberThumbnailLocked(url)
		a.thumbnailMu.Unlock()
		a.invalidate()
	}()
}

func (a *App) rememberThumbnailLocked(url string) {
	if a.thumbnails == nil {
		a.thumbnails = make(map[string]*ui.Bitmap)
	}
	if a.thumbnailFailures == nil {
		a.thumbnailFailures = make(map[string]bool)
	}
	known := false
	for _, candidate := range a.thumbnailOrder {
		if candidate == url {
			known = true
			break
		}
	}
	if !known {
		a.thumbnailOrder = append(a.thumbnailOrder, url)
	}
	for len(a.thumbnailOrder) > thumbnailCacheLimit {
		victimIndex := -1
		for i, candidate := range a.thumbnailOrder {
			if !a.thumbnailLoading[candidate] && candidate != url {
				victimIndex = i
				break
			}
		}
		if victimIndex < 0 {
			break
		}
		victim := a.thumbnailOrder[victimIndex]
		a.thumbnailOrder = append(a.thumbnailOrder[:victimIndex], a.thumbnailOrder[victimIndex+1:]...)
		delete(a.thumbnails, victim)
		delete(a.thumbnailFailures, victim)
	}
}

func (a *App) mediaCard(c *ui.Context) {
	t := c.Theme()
	m := a.currentMedia
	ui.Row(c).FillWidth().Material(glass.Glass{Clarity: 0.2}).Border(1, t.Border).Padding(14, 16).Radius(14).Gap(14).AlignItems(ui.Center).Children(func() {
		a.thumbnailElement(c, m.Thumbnail, 152, 86, appui.IconVideo)
		ui.Column(c).Grow(1).Gap(4).Children(func() {
			ui.Text(c, m.Title).FontSize(16).Bold().MaxWidth(720)
			meta := m.Uploader
			if m.Duration > 0 {
				meta += " · " + formatDuration(m.Duration)
			}
			if m.Extractor != "" {
				meta += " · " + m.Extractor
			}
			ui.Text(c, meta).FontSize(12).TextColor(t.TextMuted)
			if platform := services.DirectDownloadPlatform(mediaURL(m)); platform != "" {
				ui.Text(c, a.tr("media.directSource", map[string]any{"platform": platform})).FontSize(11).TextColor(t.Accent)
			}
		})
		ui.Column(c).Gap(5).AlignItems(ui.End).Children(func() {
			if count := len(highQualityOpusAudioTracks(m.AudioTracks)); count > 1 {
				ui.Text(c, a.tr("media.audioTracks", map[string]any{"n": count})).FontSize(11).TextColor(t.Accent)
			}
			if subCount := len(subtitleLanguages(m)); subCount > 0 {
				if subCount == 1 {
					ui.Text(c, a.tr("1 subtitle language")).FontSize(11).TextColor(t.Accent)
				} else {
					ui.Text(c, a.tr("media.subtitleMany", map[string]any{"n": subCount})).FontSize(11).TextColor(t.Accent)
				}
			}
		})
	})
}

func (a *App) formatPicker(c *ui.Context) {
	t := c.Theme()
	m := a.currentMedia
	if m == nil {
		return
	}
	if len(m.Formats) == 0 {
		ui.Collapsible(c, a.tr("Formats (optional)"), &a.formatsOpen, func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if a.formatsLoading {
					ui.Spinner(c)
					ui.Text(c, a.tr("Loading formats…")).FontSize(12).TextColor(t.TextMuted)
				} else if ui.Button(c, a.tr("Load available formats")).Clicked() {
					a.fetchFormats()
				}
			})
		})
		return
	}

	// Build dropdown option lists once per media change (labels stay stable while open).
	if len(a.videoFmtLabels) == 0 {
		a.rebuildFormatOptions()
	}

	ui.Collapsible(c, a.tr("Formats (optional)"), &a.formatsOpen, func() {
		ui.Column(c).FillWidth().Gap(8).Padding(4, 0, 0, 0).Children(func() {
			ui.Text(c, a.tr("Leave Auto unless you need a specific stream. Video+Audio always merges audio.")).FontSize(11).TextColor(t.TextMuted)

			ui.Text(c, a.tr("Video stream")).FontSize(12).TextColor(t.TextMuted)
			if ui.Select(c, &a.selectedVideoLabel, a.videoFmtLabels).Changed() {
				a.selectedVideoFormat = ""
				for i, lab := range a.videoFmtLabels {
					if lab == a.selectedVideoLabel {
						a.selectedVideoFormat = a.videoFmtIDs[i]
						if a.videoFmtIDs[i] != "" {
							// clear height cap when a concrete id is chosen
							// (selector uses the id + best audio)
						}
						break
					}
				}
			}

			ui.Text(c, a.tr("Audio stream")).FontSize(12).TextColor(t.TextMuted)
			if ui.Select(c, &a.selectedAudioLabel, a.audioFmtLabels).Changed() {
				a.selectedAudioFormat = ""
				for i, lab := range a.audioFmtLabels {
					if lab == a.selectedAudioLabel {
						a.selectedAudioFormat = a.audioFmtIDs[i]
						break
					}
				}
			}

			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, a.tr("Reset to Auto")).Clicked() {
					a.selectedVideoFormat = ""
					a.selectedAudioFormat = ""
					a.selectedVideoLabel = "Auto (best for resolution)"
					a.selectedAudioLabel = "Auto (Opus / best)"
				}
			})
		})
	})
}

func (a *App) rebuildFormatOptions() {
	m := a.currentMedia
	a.videoFmtLabels = []string{"Auto (best for resolution)"}
	a.videoFmtIDs = []string{""}
	a.audioFmtLabels = []string{"Auto (Opus / best)"}
	a.audioFmtIDs = []string{""}
	if m == nil {
		return
	}
	type row struct {
		id, label string
		height    int
		tbr       float64
		video     bool
	}
	var videos, audios []row
	for _, f := range m.Formats {
		if !f.IsVideo && !f.IsAudio {
			continue
		}
		if m.Is360 && f.IsVideo && isMeshVideoFormat(f) {
			// YouTube's progressive "mesh" streams are packed for immersive
			// playback and can appear as two stacked layers in ordinary players.
			// Keep the equirectangular HLS streams available instead.
			continue
		}
		parts := []string{f.ID}
		if f.Resolution != "" {
			parts = append(parts, f.Resolution)
		}
		if f.FPS > 0 {
			parts = append(parts, fmt.Sprintf("%.0ffps", f.FPS))
		}
		if f.Ext != "" {
			parts = append(parts, f.Ext)
		}
		if f.VCodec != "" && f.VCodec != "none" {
			parts = append(parts, shortCodec(f.VCodec))
		}
		if f.ACodec != "" && f.ACodec != "none" {
			parts = append(parts, shortCodec(f.ACodec))
		}
		size := f.Filesize
		if size == 0 {
			size = f.FilesizeApprox
		}
		if size > 0 {
			parts = append(parts, formatBytes(size))
		}
		label := strings.Join(parts, " · ")
		if f.IsVideo {
			videos = append(videos, row{f.ID, label, f.Height, f.TBR, true})
		}
		if f.IsAudio && !f.IsVideo {
			audios = append(audios, row{f.ID, label, 0, f.TBR, false})
		}
	}
	sort.SliceStable(videos, func(i, j int) bool {
		if videos[i].height != videos[j].height {
			return videos[i].height > videos[j].height
		}
		return videos[i].tbr > videos[j].tbr
	})
	sort.SliceStable(audios, func(i, j int) bool {
		return audios[i].tbr > audios[j].tbr
	})
	if len(videos) > 50 {
		videos = videos[:50]
	}
	if len(audios) > 30 {
		audios = audios[:30]
	}
	for _, r := range videos {
		a.videoFmtLabels = append(a.videoFmtLabels, r.label)
		a.videoFmtIDs = append(a.videoFmtIDs, r.id)
	}
	for _, r := range audios {
		a.audioFmtLabels = append(a.audioFmtLabels, r.label)
		a.audioFmtIDs = append(a.audioFmtIDs, r.id)
	}
}

func isMeshVideoFormat(format models.FormatInfo) bool {
	return strings.Contains(strings.ToLower(format.Note), "mesh")
}

func (a *App) configPanel(c *ui.Context) {
	t := c.Theme()
	a.syncContainerSelection()

	// ── Mode selector cards ───────────────────────────────────────────────
	a.configSectionTitle(c, a.tr("Download mode"))
	ui.Row(c).FillWidth().Gap(10).Children(func() {
		a.modeBtn(c, models.ModeVideoAudio, appui.IconVideo, a.tr("Video + Audio"), a.tr("Best quality, merged streams"))
		a.modeBtn(c, models.ModeVideoOnly, appui.IconVideo, a.tr("Video only"), a.tr("No audio track muxed"))
		a.modeBtn(c, models.ModeAudioOnly, appui.IconMusic, a.tr("Audio only"), a.tr("Extract audio stream"))
	})

	ui.Box(c).FillWidth().Height(1).Background(t.Border)

	if a.mode != models.ModeAudioOnly {
		// ── Bento: container/fps left | resolution right ──────────────────
		a.configSectionTitle(c, a.tr("Video options"))
		containerLocked := a.containerSelectionLocked()
		ct := a.container
		ui.Row(c).FillWidth().Gap(20).AlignItems(ui.Start).Children(func() {
			// left column: container
			ui.Column(c).Gap(8).Grow(5).Children(func() {
				ui.Text(c, a.tr("Container")).FontSize(12).TextColor(t.TextMuted)
				a.chipGroup(c, "container", []string{"mp4", "mkv", "webm"}, ct, containerLocked, func(opt string) {
					a.container = opt
					a.preferredContainer = opt
				})
			})
			// right column: quality
			ui.Column(c).Gap(8).Grow(7).Children(func() {
				ui.Text(c, a.tr("Max quality")).FontSize(12).TextColor(t.TextMuted)
				qLabels := []string{"Best", "2160p", "1440p", "1080p", "720p", "480p", "360p"}
				qVals := []int{0, 2160, 1440, 1080, 720, 480, 360}
				curIdx := 0
				for i, h := range qVals {
					if a.maxHeight == h {
						curIdx = i
						break
					}
				}
				a.chipGroup(c, "quality", qLabels, qLabels[curIdx], false, func(opt string) {
					for i, lbl := range qLabels {
						if lbl == opt {
							a.maxHeight = qVals[i]
							a.selectedVideoFormat = ""
							a.selectedVideoLabel = "Auto (best for resolution)"
							break
						}
					}
				})
			})
		})
		if containerLocked {
			ui.Text(c, a.tr("config.containerLocked")).FontSize(11).TextColor(t.TextMuted)
		} else if ct == "mp4" && a.settingsDraft.PreferVP9Video {
			ui.Text(c, a.tr("config.mp4VP9Note")).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Box(c).FillWidth().Height(1).Background(t.Border)
	}

	if a.mode == models.ModeAudioOnly {
		// ── Audio format & quality chips ──────────────────────────────────
		a.configSectionTitle(c, a.tr("Audio"))
		ui.Text(c, a.tr("Format")).FontSize(12).TextColor(t.TextMuted)
		a.chipGroup(c, "audioformat", []string{"mp3", "m4a", "opus", "flac", "wav"}, a.audioFormat, false, func(opt string) {
			a.audioFormat = opt
		})
		ui.Box(c).FillWidth().Height(1).Background(t.Border)
	}

	if a.mode == models.ModeVideoAudio && a.currentMedia != nil {
		a.audioTracksControls(c)
	}
	if a.mode != models.ModeAudioOnly {
		if a.hasSubtitles() {
			a.subtitleControls(c)
		}
	}
	a.syncContainerSelection()
}

// configSectionTitle renders a section heading label (matches ref headline_md).
func (a *App) configSectionTitle(c *ui.Context, text string) {
	ui.Text(c, text).FontSize(14).Bold()
}

// chipBtn renders an exclusive selectable chip. When disabled, it is greyed out
// and not clickable (used for the container selector when locked to MKV).
func (a *App) chipBtn(c *ui.Context, key, label string, selected, disabled bool, onClick func()) {
	t := c.Theme()
	bg := t.Surface
	border := t.Border
	textColor := t.TextMuted
	if selected {
		bg = t.SurfaceHover
		border = t.Accent
		textColor = t.Accent
	}
	if disabled && !selected {
		textColor = t.TextMuted
	}
	btn := ui.Row(c.Key("chip-"+key)).Background(bg).Border(1, border).Radius(8).Padding(7, 12).Grow(1).Justify(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(13).Bold().TextColor(textColor)
	})
	if btn.Clicked() && !disabled && !selected {
		onClick()
	}
}

// chipGroup renders a segmented chip-card selector inside a tonal track container,
// matching the reference ChipGroup component style.
// Each option gets an equal-width card; the selected one has an accent border and
// accent-coloured label; unselected cards are transparent.
func (a *App) chipGroup(c *ui.Context, groupKey string, options []string, selected string, disabled bool, onSelect func(string)) {
	t := c.Theme()
	ui.Row(c.Key("cg-"+groupKey)).FillWidth().Background(t.SurfaceHover).Border(1, t.Border).Radius(10).Padding(4).Gap(4).Children(func() {
		for i, opt := range options {
			opt := opt
			isSelected := opt == selected
			isDisabled := disabled && !isSelected
			var bg, border ui.Color
			var textColor ui.Color
			if isSelected {
				bg = t.SurfacePressed
				border = t.Accent
				textColor = t.Accent
			} else if isDisabled {
				textColor = t.TextMuted
			} else {
				textColor = t.Text
			}
			card := ui.Column(c.Key(fmt.Sprintf("cg-%s-%d", groupKey, i))).Grow(1).Background(bg).Border(1, border).Radius(8).Padding(8, 10).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
				ui.Text(c, opt).FontSize(13).Bold().TextColor(textColor)
			})
			if card.Clicked() && !isSelected && !isDisabled {
				onSelect(opt)
			}
		}
	})
}

func (a *App) audioTracksControls(c *ui.Context) {
	if a.currentMedia == nil {
		return
	}
	tracks := highQualityOpusAudioTracks(a.currentMedia.AudioTracks)
	if len(tracks) <= 1 {
		return
	}
	t := c.Theme()
	ui.Box(c).FillWidth().Height(1).Background(t.Border)
	ui.Column(c).FillWidth().Gap(10).Children(func() {
		a.configSectionTitle(c, a.tr("Audio tracks"))
		ui.Text(c, a.tr("Select which audio languages to include in the output file.")).FontSize(12).TextColor(t.TextMuted)
		ui.Row(c).FillWidth().Gap(6).Children(func() {
			a.selectionModeBtn(c, "audio-tracks", &a.audioTracksMode, audioTracksNone, "Default")
			a.selectionModeBtn(c, "audio-tracks", &a.audioTracksMode, audioTracksAll, "All")
			a.selectionModeBtn(c, "audio-tracks", &a.audioTracksMode, audioTracksCustom, "Custom")
		})
		if a.audioTracksMode == audioTracksCustom {
			ui.Column(c).FillWidth().Gap(4).Background(t.Surface).Border(1, t.Border).Radius(8).Padding(10).Children(func() {
				ui.Scroll(c).FillWidth().MinWidth(0).Height(168).Padding(4, 6).Children(func() {
					ui.Grid(c).FillWidth().MinWidth(0).Columns(3).GapX(14).GapY(8).Children(func() {
						for _, tr := range tracks {
							id := tr.ID
							on := checkboxState(a.selectedAudio, id)
							label := a.audioTrackLanguage(tr)
							ui.Row(c.Key("aud-tile-"+id)).FillWidth().MinWidth(0).Padding(6, 10).
								AlignItems(ui.Center).Background(t.Background).Border(1, t.Border).Radius(6).Children(func() {
								ui.Checkbox(c.Key("aud-"+id), on, label)
							})
						}
					})
				})
			})
		}
	})
}

func highQualityOpusAudioTracks(tracks []models.AudioTrack) []models.AudioTrack {
	if len(tracks) == 0 {
		return nil
	}

	bestByLang := make(map[string]models.AudioTrack)
	for _, tr := range tracks {
		langKey := audioTrackLanguageKey(tr.Language)

		current, exists := bestByLang[langKey]
		if !exists {
			bestByLang[langKey] = tr
			continue
		}

		trOpus := strings.Contains(strings.ToLower(tr.Codec), "opus")
		curOpus := strings.Contains(strings.ToLower(current.Codec), "opus")

		if trOpus && !curOpus {
			// Prefer Opus for this language
			bestByLang[langKey] = tr
		} else if trOpus == curOpus {
			// Both Opus or both non-Opus: pick higher bitrate
			if tr.Bitrate > current.Bitrate {
				bestByLang[langKey] = tr
			} else if tr.Bitrate == current.Bitrate && tr.ID > current.ID {
				bestByLang[langKey] = tr
			}
		}
		// If current is Opus and tr is not Opus, keep current Opus track
	}

	result := make([]models.AudioTrack, 0, len(bestByLang))
	for _, tr := range bestByLang {
		result = append(result, tr)
	}

	sort.Slice(result, func(i, j int) bool {
		origI := result[i].IsOriginalTrack()
		origJ := result[j].IsOriginalTrack()
		if origI != origJ {
			return origI
		}
		nameI := languageDisplayName(result[i].Language)
		if nameI == "" {
			nameI = result[i].Language
		}
		nameJ := languageDisplayName(result[j].Language)
		if nameJ == "" {
			nameJ = result[j].Language
		}
		if nameI != nameJ {
			return strings.ToLower(nameI) < strings.ToLower(nameJ)
		}
		return result[i].ID < result[j].ID
	})

	return result
}

func audioTrackLanguageKey(language string) string {
	key := strings.ToLower(strings.TrimSpace(language))
	key = strings.ReplaceAll(key, "_", "-")
	if index := strings.IndexByte(key, '-'); index > 0 {
		key = key[:index]
	}
	if key == "und" || key == "unknown" || key == "none" {
		return ""
	}
	return key
}

func (a *App) audioTrackLanguage(track models.AudioTrack) string {
	lang := languageDisplayName(track.Language)
	if lang == "" {
		clean := strings.ToLower(strings.TrimSpace(track.Language))
		if clean != "" && clean != "und" && clean != "unknown" && clean != "none" {
			lang = track.Language
		} else if track.Note != "" {
			lang = track.Note
		} else {
			lang = "Audio track " + track.ID
		}
	}
	if track.IsOriginalTrack() && !strings.Contains(strings.ToLower(lang), "original") {
		lang += " (Original)"
	}
	return lang
}

func (a *App) hasSubtitles() bool {
	if a.currentMedia == nil {
		return false
	}
	return len(subtitleLanguages(a.currentMedia)) > 0
}

func (a *App) subtitleControls(c *ui.Context) {
	if !a.hasSubtitles() {
		return
	}
	t := c.Theme()
	langs := subtitleLanguages(a.currentMedia)
	if len(langs) == 0 {
		return
	}
	ui.Box(c).FillWidth().Height(1).Background(t.Border)
	ui.Column(c).FillWidth().Gap(10).Children(func() {
		a.configSectionTitle(c, a.tr("Subtitles"))
		ui.Text(c, a.tr("Embed subtitle tracks into the output file (requires MKV for multiple).")).FontSize(12).TextColor(t.TextMuted)
		ui.Row(c).FillWidth().Gap(6).Children(func() {
			a.selectionModeBtn(c, "subtitles", &a.subtitleMode, subtitleModeNone, "None")
			a.selectionModeBtn(c, "subtitles", &a.subtitleMode, subtitleModeAll, "All")
			a.selectionModeBtn(c, "subtitles", &a.subtitleMode, subtitleModeCustom, "Custom")
		})
		if a.subtitleMode == subtitleModeCustom {
			ui.Column(c).FillWidth().Gap(4).Background(t.Surface).Border(1, t.Border).Radius(8).Padding(10).Children(func() {
				ui.Scroll(c).FillWidth().MinWidth(0).Height(168).Padding(4, 6).Children(func() {
					ui.Grid(c).FillWidth().MinWidth(0).Columns(3).GapX(14).GapY(8).Children(func() {
						for _, lang := range langs {
							on := checkboxState(a.selectedSubtitle, lang)
							label := languageDisplayName(lang)
							if label == "" {
								label = lang
							}
							ui.Row(c.Key("sub-tile-"+lang)).FillWidth().MinWidth(0).Padding(6, 10).
								AlignItems(ui.Center).Background(t.Background).Border(1, t.Border).Radius(6).Children(func() {
								ui.Checkbox(c.Key("sub-"+lang), on, label)
							})
						}
					})
				})
			})
		}
	})
}

func (a *App) selectionModeBtn(c *ui.Context, key string, selected *string, value, label string) {
	a.chipBtn(c, key+"-"+value, label, *selected == value, false, func() {
		*selected = value
	})
}

func (a *App) modeBtn(c *ui.Context, mode models.DownloadMode, icon *ui.SVG, label, desc string) {
	t := c.Theme()
	selected := a.mode == mode
	bg := t.Surface
	border := t.Border
	iconColor := t.TextMuted
	labelColor := t.Text
	if selected {
		bg = t.SurfaceHover
		border = t.Accent
		iconColor = t.Accent
	}
	btn := ui.Column(c.Key("mode-"+string(mode))).Background(bg).Border(1, border).Radius(10).Padding(16, 12).Gap(6).Grow(1).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icon).FontSize(26).TextColor(iconColor)
		ui.Text(c, label).FontSize(14).Bold().TextColor(labelColor)
		ui.Text(c, desc).FontSize(11).TextColor(t.TextMuted)
	})
	if btn.Clicked() && !selected {
		a.mode = mode
	}
}

// toggleBtn renders a PrimaryButton when selected, otherwise a normal Button.
func (a *App) toggleBtn(c *ui.Context, key, label string, selected bool, onClick func()) {
	if selected {
		ui.PrimaryButton(c.Key(key+"-on"), label)
		return
	}
	if ui.Button(c.Key(key), label).Clicked() {
		onClick()
	}
}

func (a *App) playlistPanel(c *ui.Context) {
	t := c.Theme()
	p := a.currentPlaylist
	ui.Column(c).Grow(1).Gap(10).Children(func() {
		ui.Text(c, p.Title).FontSize(15).Bold()
		ui.Text(c, a.tr("playlist.summary", map[string]any{"n": len(p.Entries), "uploader": p.Uploader})).FontSize(12).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, a.tr("Select all")).Clicked() {
				for i := range p.Entries {
					a.selectedEntries[i] = true
				}
			}
			if ui.Button(c, a.tr("Select none")).Clicked() {
				a.selectedEntries = map[int]bool{}
			}
			n := 0
			for _, v := range a.selectedEntries {
				if v {
					n++
				}
			}
			ui.Text(c, a.tr("common.selected", map[string]any{"n": n})).FontSize(12).TextColor(t.TextMuted)
			ui.Spacer(c).Grow(1)
			if a.playlistActive {
				ui.Progress(c, float64(a.playlistDone+a.playlistFailed)/float64(maxInt(a.playlistQueued, 1))).Width(160)
			}
		})
		ui.List(c, &a.plListState, len(p.Entries), func(i int) {
			ent := p.Entries[i]
			ui.Row(c).FillWidth().Padding(4, 6).Gap(8).AlignItems(ui.Center).Children(func() {
				a.playlistEntryCheckbox(c, i)
				ui.Column(c).Grow(1).Children(func() {
					ui.Text(c, ent.Title).FontSize(13)
					if ent.Duration > 0 {
						ui.Text(c, formatDuration(ent.Duration)).FontSize(11).TextColor(t.TextMuted)
					}
				})
			})
		}).Grow(1)
		if ui.PrimaryButton(c, a.tr("Download selected")).Clicked() {
			a.page = PageConfig
		}
	})
}

func (a *App) playlistEntryCheckbox(c *ui.Context, index int) {
	on := a.selectedEntries[index]
	checkbox := ui.Checkbox(c.Key(fmt.Sprintf("pe-%d-check", index)), &on, "").Label(fmt.Sprintf("playlist-entry-%d", index))
	if checkbox.Changed() {
		a.selectedEntries[index] = on
	}
}

func (a *App) configPage(c *ui.Context) {
	t := c.Theme()
	// outer column: header + scrollable body + fixed bottom bar
	ui.Column(c).Grow(1).FillWidth().Gap(0).Children(func() {
		// ── fixed header ──────────────────────────────────────────────
		ui.Row(c).FillWidth().Padding(0, 0, 10, 0).AlignItems(ui.Center).Gap(10).Children(func() {
			if ui.Button(c, a.tr("←")).Clicked() {
				a.page = PageDashboard
			}
			ui.Column(c).Gap(2).Grow(1).Children(func() {
				if a.currentMedia != nil {
					ui.Text(c, a.currentMedia.Title).FontSize(15).Bold().Ellipsis("…")
				} else if a.currentPlaylist != nil {
					n := 0
					for _, v := range a.selectedEntries {
						if v {
							n++
						}
					}
					ui.Text(c, a.tr("config.playlistSelected", map[string]any{"n": n, "title": a.currentPlaylist.Title})).FontSize(15).Bold().Ellipsis("…")
				}
			})
			if a.currentMedia != nil && a.currentMedia.Thumbnail != "" {
				a.thumbnailElement(c, a.currentMedia.Thumbnail, 85, 48, appui.IconVideo)
			}
		})

		// ── scrollable body ───────────────────────────────────────────
		ui.Scroll(c).Grow(1).Gap(0).Children(func() {
			ui.Column(c).FillWidth().Gap(20).Children(func() {
				a.configPanel(c)
				a.formatPicker(c)
			})
		})

		// ── fixed bottom action bar ───────────────────────────────────
		ui.Row(c).FillWidth().Background(t.Surface).Border(1, t.Border).Radius(10).Padding(12, 16).Gap(10).AlignItems(ui.Center).Children(func() {
			// summary chips
			if a.mode == models.ModeAudioOnly {
				a.configSummaryChip(c, strings.ToUpper(a.audioFormat))
			} else {
				a.configSummaryChip(c, strings.ToUpper(a.container))
				if a.maxHeight > 0 {
					a.configSummaryChip(c, fmt.Sprintf("%dp", a.maxHeight))
				} else {
					a.configSummaryChip(c, a.tr("Best"))
				}
			}
			ui.Spacer(c).Grow(1)
			// estimated size pill
			if est := a.estimatedSizeText(); est != "" {
				ui.Row(c).Background(t.SurfaceHover).Border(1, t.Border).Radius(6).Padding(5, 10).Gap(5).AlignItems(ui.Center).Children(func() {
					ui.Text(c, est).FontSize(12).TextColor(t.Accent)
				})
			}
			// action button
			label := a.tr("Start download")
			if a.currentPlaylist != nil {
				n := 0
				for _, v := range a.selectedEntries {
					if v {
						n++
					}
				}
				if n > 1 {
					label = a.tr("config.downloadCount", map[string]any{"n": n})
				}
			}
			if ui.PrimaryButton(c, label).Clicked() {
				if a.currentMedia != nil {
					a.startDownload()
				} else if a.currentPlaylist != nil {
					a.startPlaylistDownloads()
				}
			}
		})
	})
}

// configSummaryChip renders a small label pill in the bottom bar.
func (a *App) configSummaryChip(c *ui.Context, text string) {
	t := c.Theme()
	ui.Row(c).Background(t.SurfaceHover).Border(1, t.Border).Radius(6).Padding(4, 10).Children(func() {
		ui.Text(c, text).FontSize(12).Bold()
	})
}

// friendlyError maps common yt-dlp failures to a short, localized sentence.
// The original text stays visible underneath so nothing is lost.
func (a *App) friendlyError(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "ffmpeg not found"), strings.Contains(lower, "ffmpeg is not installed"),
		strings.Contains(lower, "dyld"), strings.Contains(lower, "library not loaded"),
		strings.Contains(lower, "image not found"):
		return a.tr("errors.ffmpegMissing")
	case strings.Contains(lower, "yt-dlp") && (strings.Contains(lower, "not found") ||
		strings.Contains(lower, "executable file not found")):
		return a.tr("errors.ytdlpMissing")
	case strings.Contains(lower, "ary plus api key") || strings.Contains(lower, "aryplus_api_key"):
		return a.tr("errors.aryPlusKey")
	case strings.Contains(lower, "without reporting a single final media file"):
		return a.tr("errors.outputAmbiguous")
	case strings.Contains(lower, "unsupported url"):
		return a.tr("errors.unsupported")
	case strings.Contains(lower, "http error 403"), strings.Contains(lower, "403: forbidden"):
		return a.tr("errors.forbidden")
	case strings.Contains(lower, "http error 429"), strings.Contains(lower, "http 429"), strings.Contains(lower, "too many requests"):
		return a.tr("errors.rateLimited")
	case strings.Contains(lower, "no space left"):
		return a.tr("errors.disk")
	case strings.Contains(lower, "network"), strings.Contains(lower, "timed out"), strings.Contains(lower, "connection"),
		strings.Contains(lower, "load page: request failed"):
		return a.tr("errors.network")
	}
	return message
}

func (a *App) doAnalyze(url string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result, err := a.ytdlp.Analyze(ctx, url)
	if err != nil {
		a.updateOnMain(func() {
			a.analyzing = false
			a.analyzeError = err.Error()
			a.analyzeUnsupported = errors.Is(err, services.ErrUnsupportedURL)
			a.invalidate()
		})
		return
	}
	a.updateOnMain(func() {
		switch v := result.(type) {
		case *models.MediaInfo:
			a.currentMedia = v
		case *models.PlaylistInfo:
			a.currentPlaylist = v
		}
		a.analyzing = false
		a.invalidate()
	})
}

func (a *App) fetchFormats() {
	if a.currentMedia == nil || a.formatsLoading {
		return
	}
	a.formatsLoading = true
	a.invalidate()
	url := a.currentMedia.WebpageURL
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		full, err := a.ytdlp.FetchFormats(ctx, url)
		a.updateOnMain(func() {
			if err != nil {
				a.analyzeError = err.Error()
			} else if a.currentMedia != nil && a.currentMedia.WebpageURL == url {
				a.currentMedia.Formats = full.Formats
				a.currentMedia.Subtitles = full.Subtitles
				a.currentMedia.AudioTracks = full.AudioTracks
				a.rebuildFormatOptions()
			}
			a.formatsLoading = false
			a.invalidate()
		})
	}()
}

func (a *App) startDownload() {
	if a.currentMedia == nil {
		return
	}
	if !a.requireTools(true, false) {
		return
	}
	req := a.buildRequest(mediaURL(a.currentMedia), a.currentMedia.Title, a.currentMedia.Thumbnail, "", 0)
	req.SourcePageURL = a.currentMedia.SourcePageURL
	req.SourceIndex = a.currentMedia.SourceIndex
	a.page = PageDownloads
	a.showToast(a.tr("Queueing: ") + a.currentMedia.Title)
	go func() {
		_, err := a.manager.EnqueueContext(context.Background(), req)
		a.updateOnMain(func() {
			if err != nil {
				a.showToast(a.tr("Could not queue download: ") + err.Error())
				return
			}
			a.showToast(a.tr("Queued: ") + req.Title)
		})
	}()
}

func (a *App) isDirectHDMedia() bool {
	return a.currentMedia != nil && services.IsOneClickDownloadURL(mediaURL(a.currentMedia))
}

func (a *App) startDirectHDDownload() {
	if !a.isDirectHDMedia() {
		return
	}
	if !a.requireTools(true, false) {
		return
	}
	req := a.buildDirectHDRequest()
	a.page = PageDownloads
	a.showToast(a.tr("Queueing HD download: ") + req.Title)
	go func() {
		_, err := a.manager.EnqueueContext(context.Background(), req)
		a.updateOnMain(func() {
			if err != nil {
				a.showToast(a.tr("Could not queue download: ") + err.Error())
				return
			}
			a.showToast(a.tr("Queued HD download: ") + req.Title)
		})
	}()
}

func (a *App) buildDirectHDRequest() models.DownloadRequest {
	if a.currentMedia == nil {
		return models.DownloadRequest{}
	}
	settings := a.settingsDraftSnapshot()
	return models.DownloadRequest{
		URL:            mediaURL(a.currentMedia),
		Title:          a.currentMedia.Title,
		Mode:           models.ModeVideoAudio,
		OutputDir:      settings.DownloadDir,
		Container:      "mp4",
		NoCookies:      true,
		ThumbnailURL:   a.currentMedia.Thumbnail,
		EmbedThumbnail: false,
		EmbedMetadata:  false,
	}
}

func mediaURL(media *models.MediaInfo) string {
	if media == nil {
		return ""
	}
	if value := strings.TrimSpace(media.WebpageURL); value != "" {
		return value
	}
	return strings.TrimSpace(media.OriginalURL)
}

func (a *App) startPlaylistDownloads() {
	if a.currentPlaylist == nil {
		return
	}
	if !a.requireTools(true, false) {
		return
	}
	requests := make([]models.DownloadRequest, 0, len(a.currentPlaylist.Entries))
	for i, ent := range a.currentPlaylist.Entries {
		if !a.selectedEntries[i] || ent.URL == "" {
			continue
		}
		req := a.buildRequest(ent.URL, ent.Title, ent.Thumbnail, a.currentPlaylist.Title, ent.Index)
		req.SourcePageURL = ent.SourcePageURL
		req.SourceIndex = ent.SourceIndex
		requests = append(requests, req)
	}
	if len(requests) == 0 {
		a.showToast(a.tr("No videos selected"))
		return
	}
	a.playlistBatchID++
	if a.playlistBatchID == 0 {
		a.playlistBatchID++
	}
	batchID := a.playlistBatchID
	for i := range requests {
		requests[i].PlaylistBatchID = batchID
	}
	a.playlistQueued = len(requests)
	a.playlistDone = 0
	a.playlistFailed = 0
	a.playlistActive = true
	a.page = PageDownloads
	a.showToast(a.tr("downloads.queueingMany", map[string]any{"n": len(requests)}))
	go func() {
		for _, req := range requests {
			_, err := a.manager.EnqueueContext(context.Background(), req)
			if err != nil {
				a.updateOnMain(func() {
					if a.playlistActive && a.playlistBatchID == batchID {
						a.playlistFailed++
						if a.playlistDone+a.playlistFailed >= a.playlistQueued {
							a.playlistActive = false
						}
					}
					a.showToast(a.tr("Could not queue playlist item: ") + err.Error())
				})
			}
		}
		a.updateOnMain(func() {
			if a.playlistBatchID != batchID {
				return
			}
			if a.playlistDone+a.playlistFailed >= a.playlistQueued {
				a.playlistActive = false
			} else {
				a.showToast(a.tr("downloads.queuedMany", map[string]any{"n": a.playlistQueued - a.playlistFailed}))
			}
		})
	}()
}

func (a *App) containerSelectionLocked() bool {
	// Audio-track selection only affects video+audio downloads. The zero mode
	// is treated as video+audio for older callers.
	if a.mode == models.ModeVideoAudio || a.mode == "" {
		if a.audioTracksMode == audioTracksAll {
			return true
		}
		if a.audioTracksMode == audioTracksCustom && countSelected(a.selectedAudio) > 1 {
			return true
		}
	}
	if a.mode == models.ModeAudioOnly {
		return false
	}
	if a.subtitleMode == subtitleModeAll {
		return true
	}
	return a.subtitleMode == subtitleModeCustom && countSelected(a.selectedSubtitle) > 0
}

func (a *App) syncContainerSelection() {
	if a.preferredContainer == "" {
		a.preferredContainer = a.container
		if a.preferredContainer == "" {
			a.preferredContainer = "mp4"
		}
	}
	if a.containerSelectionLocked() {
		a.container = "mkv"
		return
	}
	if a.container == "mkv" && a.preferredContainer != "mkv" {
		a.container = a.preferredContainer
	}
}

func countSelected(states map[string]*bool) int {
	n := 0
	for _, state := range states {
		if state != nil && *state {
			n++
		}
	}
	return n
}

func (a *App) estimatedSizeText() string {
	if a.currentMedia == nil {
		return ""
	}
	size, approximate := a.estimatedFinalSize()
	if size <= 0 {
		return ""
	}
	marker := ""
	if approximate {
		marker = "~"
	}
	return a.tr("Estimated final size: ") + marker + formatBytes(size)
}

func (a *App) estimatedFinalSize() (int64, bool) {
	m := a.currentMedia
	if m == nil {
		return 0, false
	}
	settings := a.settingsDraftSnapshot()
	var total int64
	approximate := false
	seenFormats := make(map[string]bool)
	addFormat := func(format *models.FormatInfo) {
		if format == nil {
			return
		}
		if format.ID != "" && seenFormats[format.ID] {
			return
		}
		if format.ID != "" {
			seenFormats[format.ID] = true
		}
		size, estimated := estimateFormatSize(*format, m.Duration)
		total += size
		approximate = approximate || estimated
	}
	addAudioTrack := func(track models.AudioTrack) {
		if format := findFormat(m.Formats, track.ID); format != nil {
			addFormat(format)
			return
		}
		if track.Bitrate > 0 && m.Duration > 0 {
			total += int64(track.Bitrate * 1000 / 8 * m.Duration)
			approximate = true
		}
	}

	if a.mode != models.ModeAudioOnly {
		video := findFormat(m.Formats, a.selectedVideoFormat)
		if video == nil {
			video = bestVideoFormatForRequest(m.Formats, a.maxHeight, a.container, settings.PreferVP9Video)
		}
		addFormat(video)
		if a.mode == models.ModeVideoOnly {
			return total, markFinalSizeApproximate(a, approximate)
		}

		// An automatic muxed fallback already contains audio. Do not add the
		// default audio stream a second time. Explicit format choices are
		// different: yt-dlp intentionally appends the selected audio stream.
		useEmbeddedAudio := a.selectedVideoFormat == "" && formatIsMuxed(video) &&
			a.audioTracksMode == audioTracksNone && a.selectedAudioFormat == ""
		if !useEmbeddedAudio {
			switch a.audioTracksMode {
			case audioTracksAll:
				for _, track := range m.AudioTracks {
					addAudioTrack(track)
				}
			case audioTracksCustom:
				selected := 0
				for id, on := range a.selectedAudio {
					if on == nil || !*on {
						continue
					}
					selected++
					if track := findAudioTrack(m.AudioTracks, id); track != nil {
						addAudioTrack(*track)
					} else if format := findFormat(m.Formats, id); format != nil {
						addFormat(format)
					}
				}
				if selected == 0 {
					addFormat(bestAudioFormatForRequest(m.Formats, a.container))
				}
			default:
				format := findFormat(m.Formats, a.selectedAudioFormat)
				if format == nil {
					format = bestAudioFormatForRequest(m.Formats, a.container)
				}
				addFormat(format)
			}
		}
	}
	if a.mode == models.ModeAudioOnly {
		format := findFormat(m.Formats, a.selectedAudioFormat)
		if format == nil {
			format = bestAudioFormatForRequest(m.Formats, a.container)
		}
		addFormat(format)
	}
	return total, markFinalSizeApproximate(a, approximate)
}

func markFinalSizeApproximate(a *App, approximate bool) bool {
	settings := a.settingsDraftSnapshot()
	if a.mode == models.ModeVideoAudio || a.subtitleMode != subtitleModeNone ||
		settings.EmbedMetadata || settings.EmbedThumbnail {
		return true
	}
	return approximate
}

func findFormat(formats []models.FormatInfo, id string) *models.FormatInfo {
	if id == "" {
		return nil
	}
	for i := range formats {
		if formats[i].ID == id {
			return &formats[i]
		}
	}
	return nil
}

func findAudioTrack(tracks []models.AudioTrack, id string) *models.AudioTrack {
	for i := range tracks {
		if tracks[i].ID == id {
			return &tracks[i]
		}
	}
	return nil
}

func bestVideoFormatForRequest(formats []models.FormatInfo, maxHeight int, container string, preferVP9 bool) *models.FormatInfo {
	candidates := make([]*models.FormatInfo, 0, len(formats))
	for i := range formats {
		format := &formats[i]
		if !format.IsVideo {
			continue
		}
		height := formatHeight(*format)
		if maxHeight > 0 && height > maxHeight {
			continue
		}
		candidates = append(candidates, format)
	}
	if len(candidates) == 0 {
		return nil
	}

	// Prefer a video-only stream whenever it exists. A muxed format is a
	// fallback, not an independent video payload whose audio can be counted
	// again below.
	videoOnly := filterFormats(candidates, func(format *models.FormatInfo) bool {
		return !formatIsMuxed(format)
	})
	if len(videoOnly) > 0 {
		candidates = videoOnly
	}

	container = models.NormalizeContainer(container)
	vp9Preferred := models.PreferVP9ForContainer(container, preferVP9)
	switch container {
	case "mp4":
		mp4 := filterFormats(candidates, func(format *models.FormatInfo) bool {
			return strings.EqualFold(format.Ext, "mp4")
		})
		if len(mp4) > 0 {
			candidates = mp4
		}
	case "webm":
		webm := filterFormats(candidates, func(format *models.FormatInfo) bool {
			return strings.EqualFold(format.Ext, "webm")
		})
		if len(webm) > 0 {
			candidates = webm
		}
	}

	// Mirror the download's res-first --format-sort. Filtering to VP9 here
	// would estimate a 1080p VP9 stream while yt-dlp downloads a 2160p H.264
	// or AV1 stream; VP9 is only a tie-breaker at equal resolution and FPS.
	best := candidates[0]
	for _, format := range candidates[1:] {
		bestHeight, formatHeightValue := formatHeight(*best), formatHeight(*format)
		switch {
		case formatHeightValue > bestHeight:
			best = format
		case formatHeightValue < bestHeight:
			continue
		case format.FPS > best.FPS:
			best = format
		case format.FPS < best.FPS:
			continue
		case vp9Preferred && isVP9Format(*format) != isVP9Format(*best):
			if isVP9Format(*format) {
				best = format
			}
		case format.TBR > best.TBR:
			best = format
		}
	}
	return best
}

func isVP9Format(format models.FormatInfo) bool {
	return strings.Contains(strings.ToLower(format.VCodec), "vp9")
}

func filterFormats(formats []*models.FormatInfo, keep func(*models.FormatInfo) bool) []*models.FormatInfo {
	filtered := make([]*models.FormatInfo, 0, len(formats))
	for _, format := range formats {
		if keep(format) {
			filtered = append(filtered, format)
		}
	}
	return filtered
}

func formatIsMuxed(format *models.FormatInfo) bool {
	return format != nil && format.IsVideo && format.IsAudio
}

func formatIsAudioOnly(format *models.FormatInfo) bool {
	return format != nil && format.IsAudio && !format.IsVideo
}

func formatIsVideoOnly(format *models.FormatInfo) bool {
	return format != nil && format.IsVideo && !format.IsAudio
}

func bestAudioFormatForRequest(formats []models.FormatInfo, container string) *models.FormatInfo {
	candidates := make([]*models.FormatInfo, 0, len(formats))
	for i := range formats {
		if formatIsAudioOnly(&formats[i]) {
			candidates = append(candidates, &formats[i])
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	container = models.NormalizeContainer(container)
	preferredExt := ""
	preferredCodec := ""
	if container == "mp4" {
		preferredExt = "m4a"
	} else {
		preferredCodec = "opus"
	}
	if preferredExt != "" || preferredCodec != "" {
		preferred := filterFormats(candidates, func(format *models.FormatInfo) bool {
			return (preferredExt != "" && strings.EqualFold(format.Ext, preferredExt)) ||
				(preferredCodec != "" && strings.Contains(strings.ToLower(format.ACodec), preferredCodec))
		})
		if len(preferred) > 0 {
			candidates = preferred
		}
	}

	best := candidates[0]
	for _, format := range candidates[1:] {
		if audioBitrate(*format) > audioBitrate(*best) {
			best = format
		}
	}
	return best
}

func estimateFormatSize(format models.FormatInfo, duration float64) (int64, bool) {
	if format.Filesize > 0 {
		return format.Filesize, false
	}
	if format.FilesizeApprox > 0 {
		return format.FilesizeApprox, true
	}
	if duration <= 0 {
		return 0, true
	}
	bitrate := format.TBR
	if formatIsVideoOnly(&format) && format.VBR > 0 {
		bitrate = format.VBR
	}
	if formatIsAudioOnly(&format) && format.ABR > 0 {
		bitrate = format.ABR
	}
	if formatIsMuxed(&format) && bitrate <= 0 {
		bitrate = format.VBR + format.ABR
	}
	if bitrate <= 0 {
		bitrate = format.VBR + format.ABR
	}
	if bitrate <= 0 {
		return 0, true
	}
	return int64(bitrate * 1000 / 8 * duration), true
}

func audioBitrate(format models.FormatInfo) float64 {
	if format.ABR > 0 {
		return format.ABR
	}
	if format.TBR > 0 {
		return format.TBR
	}
	return format.VBR
}

func formatHeight(format models.FormatInfo) int {
	if format.Height > 0 {
		return format.Height
	}
	resolution := strings.ToLower(strings.TrimSpace(format.Resolution))
	if idx := strings.LastIndex(resolution, "x"); idx >= 0 {
		if height, err := strconv.Atoi(strings.TrimSpace(resolution[idx+1:])); err == nil {
			return height
		}
	}
	if idx := strings.Index(resolution, "p"); idx > 0 {
		if height, err := strconv.Atoi(strings.TrimSpace(resolution[:idx])); err == nil {
			return height
		}
	}
	return 0
}

func (a *App) buildRequest(url, title, thumb, playlistTitle string, index int) models.DownloadRequest {
	a.syncContainerSelection()
	settings := a.settingsDraftSnapshot()
	a.publishSettingsDraft()
	expectedSize, expectedSizeApproximate := a.estimatedFinalSize()
	req := models.DownloadRequest{
		URL: url, Title: title, Mode: a.mode, OutputDir: settings.DownloadDir,
		Container: a.container, MaxHeight: a.maxHeight, AudioFormat: a.audioFormat,
		AudioBitrateKbps: 0, // 0 = best available (yt-dlp ba* / opus preferred)
		EmbedThumbnail:   settings.EmbedThumbnail, EmbedMetadata: settings.EmbedMetadata,
		PreferVP9: settings.PreferVP9Video, Is360: a.currentMedia != nil && a.currentMedia.Is360, ThumbnailURL: thumb,
		ExpectedSizeBytes: expectedSize, ExpectedSizeIsApproximate: expectedSizeApproximate,
		PlaylistTitle: playlistTitle, PlaylistIndex: index,
		VideoFormatID: a.selectedVideoFormat, AudioFormatID: a.selectedAudioFormat,
		AudioSelectionMode:    models.AudioSelectionMode(a.audioTracksMode),
		AudioSelection:        models.AudioSelectionMode(a.audioTracksMode),
		SubtitleSelectionMode: models.SubtitleSelectionMode(a.subtitleMode),
		SubtitleSelection:     models.SubtitleSelectionMode(a.subtitleMode),
	}
	switch a.audioTracksMode {
	case audioTracksAll:
		req.MultiAudio = true
	case audioTracksCustom:
		var selectedIDs []string
		for id, on := range a.selectedAudio {
			if on != nil && *on {
				selectedIDs = append(selectedIDs, id)
			}
		}
		req.SelectedAudioTrackIDs = a.sortSelectedAudioTrackIDs(selectedIDs)
	}
	switch a.subtitleMode {
	case subtitleModeAll:
		req.WriteSubtitles = true
		req.SubtitleLangs = []string{"all"}
	case subtitleModeCustom:
		req.WriteSubtitles = true
		var selectedLangs []string
		for lang, on := range a.selectedSubtitle {
			if on != nil && *on {
				selectedLangs = append(selectedLangs, lang)
			}
		}
		req.SubtitleLangs = sortSelectedSubtitleLangs(selectedLangs)
	}
	return req
}

func (a *App) sortSelectedAudioTrackIDs(selectedIDs []string) []string {
	if len(selectedIDs) <= 1 {
		return selectedIDs
	}
	var tracks []models.AudioTrack
	if a.currentMedia != nil {
		tracks = a.currentMedia.AudioTracks
	}
	result := append([]string(nil), selectedIDs...)
	sort.Slice(result, func(i, j int) bool {
		trI := findAudioTrack(tracks, result[i])
		trJ := findAudioTrack(tracks, result[j])
		origI := trI != nil && trI.IsOriginalTrack()
		origJ := trJ != nil && trJ.IsOriginalTrack()
		if origI != origJ {
			return origI
		}
		nameI := ""
		if trI != nil {
			nameI = a.audioTrackLanguage(*trI)
		}
		if nameI == "" {
			nameI = result[i]
		}
		nameJ := ""
		if trJ != nil {
			nameJ = a.audioTrackLanguage(*trJ)
		}
		if nameJ == "" {
			nameJ = result[j]
		}
		if !strings.EqualFold(nameI, nameJ) {
			return strings.ToLower(nameI) < strings.ToLower(nameJ)
		}
		return result[i] < result[j]
	})
	return result
}

func isEnglishSubtitle(lang string) bool {
	clean := strings.ToLower(strings.TrimSpace(lang))
	if clean == "en" || clean == "eng" || strings.HasPrefix(clean, "en-") || strings.HasPrefix(clean, "en_") {
		return true
	}
	return strings.EqualFold(languageDisplayName(clean), "English")
}

func sortSelectedSubtitleLangs(langs []string) []string {
	if len(langs) <= 1 {
		return langs
	}
	result := append([]string(nil), langs...)
	sort.Slice(result, func(i, j int) bool {
		enI := isEnglishSubtitle(result[i])
		enJ := isEnglishSubtitle(result[j])
		if enI != enJ {
			return enI
		}
		nameI := languageDisplayName(result[i])
		if nameI == "" {
			nameI = result[i]
		}
		nameJ := languageDisplayName(result[j])
		if nameJ == "" {
			nameJ = result[j]
		}
		if !strings.EqualFold(nameI, nameJ) {
			return strings.ToLower(nameI) < strings.ToLower(nameJ)
		}
		return result[i] < result[j]
	})
	return result
}

func (a *App) downloadsPage(c *ui.Context) {
	t := c.Theme()
	a.tasksMu.Lock()
	tasks := append([]models.DownloadTaskSnapshot(nil), a.tasks...)
	a.tasksMu.Unlock()
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.After(tasks[j].CreatedAt) })
	if len(tasks) == 0 {
		emptyStateAction(c, appui.IconDownload, a.tr("downloads.empty.title"), a.tr("downloads.empty.detail"), a.tr("downloads.empty.action"), func() {
			a.page = PageDashboard
		})
		return
	}
	active, queued, paused, finished := 0, 0, 0, 0
	for _, task := range tasks {
		switch task.State {
		case models.StatePending:
			queued++
		case models.StatePaused:
			paused++
		case models.StateCompleted, models.StateError, models.StateCancelled:
			finished++
		default:
			active++
		}
	}
	ui.Column(c).Grow(1).Gap(12).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, a.tr("downloads.stats", map[string]any{"active": active, "queued": queued, "paused": paused, "finished": finished})).
				FontSize(12).TextColor(t.TextMuted)
			ui.Spacer(c).Grow(1)
			if finished > 0 && ui.Button(c, a.tr("Clear finished")).Clicked() {
				a.manager.ClearFinished()
				a.invalidate()
			}
		})
		ui.List(c, &a.dlListState, len(tasks), func(i int) {
			task := tasks[i]
			ui.Row(c).FillWidth().Material(glass.Glass{Clarity: 0.2}).Border(1, t.Border).Padding(10, 12).Radius(12).Gap(12).AlignItems(ui.Center).Children(func() {
				a.thumbnailElement(c, task.Request.ThumbnailURL, 112, 63, downloadFallbackIcon(task.Request.Mode))
				ui.Column(c).Grow(1).Gap(5).Children(func() {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
						ui.Text(c, task.Request.Title).FontSize(13).Bold().Grow(1).MaxWidth(680)
						statusPill(c, task.State)
					})
					if task.State == models.StatePreparing || task.State == models.StateDownloading || task.State == models.StateProcessing || task.State == models.StatePaused {
						pct := float64(-1)
						if fraction, ok := taskProgressFraction(task.State, task.Progress); ok {
							pct = fraction
						}
						ui.Progress(c, pct)
						ui.Row(c).FillWidth().Children(func() {
							ui.Text(c, a.downloadMetrics(task.Progress)).FontSize(11).TextColor(t.TextMuted)
							ui.Spacer(c).Grow(1)
						})
					}
					if task.State == models.StateError && task.Error != "" {
						friendly := a.friendlyError(task.Error)
						ui.Text(c, friendly).FontSize(11).TextColor(t.Danger).MaxWidth(720)
						if friendly != task.Error {
							ui.Text(c, task.Error).FontSize(10).TextColor(t.TextMuted).MaxWidth(720)
						}
					}
					if task.ErrorDetail != "" {
						ui.Text(c, task.ErrorDetail).FontSize(11).TextColor(t.Warning).MaxWidth(720)
					}
					ui.Row(c).Gap(6).Children(func() {
						if task.State == models.StatePreparing || task.State == models.StateDownloading || task.State == models.StateProcessing {
							if ui.Button(c.Key("pause-"+task.ID), "Pause").Clicked() {
								a.manager.Pause(task.ID)
							}
						}
						if task.State == models.StatePaused {
							if ui.Button(c.Key("resume-"+task.ID), "Resume").Clicked() {
								if a.requireTools(true, false) {
									a.manager.Resume(task.ID)
								}
							}
						}
						if !task.State.IsTerminal() {
							if ui.Button(c.Key("cancel-"+task.ID), "Cancel").Clicked() {
								a.manager.Cancel(task.ID)
							}
						}
						if task.State == models.StateError || task.State == models.StateCancelled {
							if ui.Button(c.Key("retry-"+task.ID), "Retry").Clicked() {
								if a.requireTools(true, false) {
									a.manager.Retry(task.ID)
								}
							}
						}
						if task.State == models.StateCompleted && task.OutputPath != "" {
							ui.Text(c, filepath.Base(task.OutputPath)).FontSize(11).TextColor(t.TextMuted).Grow(1)
							path := task.OutputPath
							if ui.Button(c.Key("show-"+task.ID), "Show in folder").Clicked() {
								go mygo.Shell.ShowItemInFolder(path)
							}
						}
					})
				})
			})
		}).Grow(1).Gap(8)
	})
}

func stateColor(c *ui.Context, s models.DownloadState) ui.Color {
	t := c.Theme()
	switch s {
	case models.StateCompleted:
		return t.Success
	case models.StateError:
		return t.Danger
	case models.StateDownloading:
		return t.Accent
	case models.StateProcessing:
		return appui.ColorTeal
	case models.StatePreparing:
		return t.Warning
	case models.StatePaused:
		return t.Warning
	default:
		return t.TextMuted
	}
}

func statusPill(c *ui.Context, state models.DownloadState) {
	label := strings.Title(string(state))
	color := stateColor(c, state)
	ui.Row(c).Background(ui.Color{R: color.R, G: color.G, B: color.B, A: 0x22}).Border(1, ui.Color{R: color.R, G: color.G, B: color.B, A: 0x55}).Radius(999).Padding(3, 8).Children(func() {
		ui.Text(c, label).FontSize(10).Bold().TextColor(color)
	})
}

// emptyStateAction is emptyState with the one action that gets the user out
// of the empty screen.
func emptyStateAction(c *ui.Context, icon *ui.SVG, title, detail, action string, onClick func()) {
	t := c.Theme()
	ui.Column(c).Grow(1).Center().Gap(10).Children(func() {
		ui.Box(c).Size(56, 56).Material(glass.Glass{Clarity: 0.3}).Border(1, t.Border).Radius(18).Center().Children(func() {
			ui.Icon(c, icon).FontSize(24).TextColor(t.Accent)
		})
		ui.Text(c, title).FontSize(16).Bold()
		ui.Text(c, detail).FontSize(12).TextColor(t.TextMuted).MaxWidth(360)
		if ui.PrimaryButton(c, action).Clicked() {
			onClick()
			c.Invalidate()
		}
	})
}

func emptyState(c *ui.Context, icon *ui.SVG, title, detail string) {
	t := c.Theme()
	ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
		ui.Box(c).Size(56, 56).Material(glass.Glass{Clarity: 0.3}).Border(1, t.Border).Radius(18).Center().Children(func() {
			ui.Icon(c, icon).FontSize(24).TextColor(t.Accent)
		})
		ui.Text(c, title).FontSize(16).Bold()
		ui.Text(c, detail).FontSize(12).TextColor(t.TextMuted).MaxWidth(360)
	})
}

func downloadFallbackIcon(mode models.DownloadMode) *ui.SVG {
	if mode == models.ModeAudioOnly {
		return appui.IconMusic
	}
	return appui.IconVideo
}

func conversionDownloadState(state models.ConversionState) models.DownloadState {
	switch state {
	case models.ConvCompleted:
		return models.StateCompleted
	case models.ConvError:
		return models.StateError
	case models.ConvCancelled:
		return models.StateCancelled
	case models.ConvRunning:
		return models.StateProcessing
	default:
		return models.StatePending
	}
}

func historyDownloadState(state string) models.DownloadState {
	switch state {
	case string(models.StateCompleted):
		return models.StateCompleted
	case string(models.StateError):
		return models.StateError
	case string(models.StateCancelled):
		return models.StateCancelled
	default:
		return models.StatePending
	}
}

func historyMode(mode string) models.DownloadMode {
	if mode == string(models.ModeAudioOnly) {
		return models.ModeAudioOnly
	}
	if mode == string(models.ModeVideoOnly) {
		return models.ModeVideoOnly
	}
	return models.ModeVideoAudio
}

func historyDownloadRequest(entry services.HistoryEntry, outputDir string) models.DownloadRequest {
	return models.DownloadRequest{
		URL: entry.URL, Title: entry.Title, Mode: historyMode(entry.Mode), OutputDir: outputDir,
		Container: entry.Container, MaxHeight: entry.MaxHeight, MaxFPS: entry.MaxFPS,
		VideoFormatID: entry.VideoFormatID, AudioFormatID: entry.AudioFormatID,
		AudioFormat: entry.AudioFmt, SourcePageURL: entry.SourcePageURL, SourceIndex: entry.SourceIndex,
	}
}

func restoreHistoryVideoQuality(req *models.DownloadRequest, entry services.HistoryEntry, ffmpeg *services.FFmpegService) error {
	if req.Mode == models.ModeAudioOnly || req.MaxHeight > 0 || req.VideoFormatID != "" {
		return nil
	}
	if entry.OutputPath == "" {
		return fmt.Errorf("original downloaded file is unavailable")
	}
	if ffmpeg == nil {
		return fmt.Errorf("ffprobe is unavailable")
	}
	height, err := ffmpeg.ProbeVideoHeight(context.Background(), entry.OutputPath)
	if err != nil {
		return err
	}
	req.MaxHeight = height
	return nil
}

func progressFraction(p models.ProgressInfo) (float64, bool) {
	if p.TotalBytes > 0 && p.DownloadedBytes >= 0 {
		return clampProgress(float64(p.DownloadedBytes) / float64(p.TotalBytes)), true
	}
	if p.Percent > 0 {
		return clampProgress(p.Percent / 100), true
	}
	return 0, false
}

func taskProgressFraction(state models.DownloadState, p models.ProgressInfo) (float64, bool) {
	if state == models.StateProcessing {
		return 0, false
	}
	return progressFraction(p)
}

func progressPercent(p models.ProgressInfo) float64 {
	if fraction, ok := progressFraction(p); ok {
		return fraction * 100
	}
	return 0
}

func clampProgress(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func (a *App) downloadMetrics(p models.ProgressInfo) string {
	parts := make([]string, 0, 4)
	if _, ok := progressFraction(p); ok {
		parts = append(parts, fmt.Sprintf("%.1f%%", progressPercent(p)))
	}
	if p.TotalBytes > 0 {
		total := formatBytes(p.TotalBytes)
		if p.TotalIsEstimate {
			total = "~" + total
		}
		parts = append(parts, formatBytes(p.DownloadedBytes)+" / "+total)
	} else {
		parts = append(parts, formatBytes(p.DownloadedBytes)+" / "+a.tr("size unknown"))
	}
	if p.SpeedBps > 0 {
		parts = append(parts, formatSpeed(p.SpeedBps))
	} else {
		parts = append(parts, a.tr("speed calculating"))
	}
	if p.ETA > 0 {
		parts = append(parts, a.tr("ETA")+" "+formatDuration(p.ETA.Seconds()))
	} else {
		parts = append(parts, a.tr("ETA calculating"))
	}
	return strings.Join(parts, " · ")
}

func (a *App) converterPage(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).Gap(12).Children(func() {
		ui.Text(c, a.tr("Remux or re-encode your local media with FFmpeg.")).FontSize(12).TextColor(t.TextMuted)
		if !a.ffmpeg.Available() {
			ui.Row(c).Background(t.Surface).Border(1, t.Danger).Radius(12).Padding(12).Children(func() {
				ui.Text(c, a.tr("converter.ffmpegMissing")).FontSize(13).TextColor(t.Danger)
			})
			return
		}
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Grow(7).Material(glass.Glass{Clarity: 0.2}).Border(1, t.Border).Padding(14).Radius(14).Gap(10).Children(func() {
				// Drop zone + path + Browse
				drop := ui.Column(c).FillWidth().Padding(16).Radius(8).
					Border(1, t.Border).AlignItems(ui.Center).Gap(6)
				if drop.FileDragOver() {
					drop.Border(2, t.Accent)
				}
				if files := drop.DroppedFiles(); len(files) > 0 {
					a.convSource = files[0]
				}
				drop.Children(func() {
					ui.Text(c, a.tr("Drop a media file here")).FontSize(13).TextColor(t.TextMuted)
					ui.Text(c, a.tr("or use Browse")).FontSize(11).TextColor(t.TextMuted)
				})
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, a.tr("Source")).FontSize(12).TextColor(t.TextMuted).Width(90)
					ui.TextInput(c, &a.convSource).Placeholder(a.tr("/path/to/file.mp4")).Grow(1)
					if ui.Button(c, a.tr("Browse…")).Clicked() {
						go a.pickConvertFile()
					}
				})
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, a.tr("Kind")).FontSize(12).TextColor(t.TextMuted).Width(90)
					a.toggleBtn(c, "ck-a", "Audio", a.convKind == models.KindAudio, func() {
						a.convKind = models.KindAudio
						a.convFormat = "mp3"
						a.convAudioBitrateKbps = 320
					})
					a.toggleBtn(c, "ck-v", "Video", a.convKind == models.KindVideo, func() {
						a.convKind = models.KindVideo
						a.convFormat = "mp4"
					})
				})
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, a.tr("Target")).FontSize(12).TextColor(t.TextMuted).Width(90)
					formats := []string{"mp3", "m4a", "aac", "opus", "flac", "wav"}
					if a.convKind == models.KindVideo {
						formats = []string{"mp4", "mkv", "webm", "avi", "mov", "ts"}
					}
					for _, f := range formats {
						f := f
						a.toggleBtn(c, "cf-"+f, f, a.convFormat == f, func() {
							a.convFormat = f
						})
					}
				})
				if a.convKind == models.KindAudio {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, a.tr("Audio bitrate")).FontSize(12).TextColor(t.TextMuted).Width(90)
						for _, bitrate := range []int{128, 192, 256, 320} {
							bitrate := bitrate
							a.toggleBtn(c, fmt.Sprintf("conv-bitrate-%d", bitrate), fmt.Sprintf("%d kbps", bitrate), a.convAudioBitrateKbps == bitrate, func() {
								a.convAudioBitrateKbps = bitrate
							})
						}
					})
					if a.convFormat == "flac" || a.convFormat == "wav" {
						ui.Text(c, a.tr("Lossless output ignores bitrate selection.")).FontSize(11).TextColor(t.TextMuted)
					}
				}
				if strings.TrimSpace(a.convSource) != "" {
					if a.convKind == models.KindVideo {
						remux, checking, known := a.remuxDecision(a.convSource, a.convFormat)
						label, color := "Checking compatibility…", t.TextMuted
						if known && remux {
							label, color = "Lossless remux", t.Success
						} else if known && !remux {
							label, color = a.tr("Re-encode"), t.Warning
						} else if !checking {
							label, color = a.tr("Re-encode"), t.Warning
						}
						ui.Row(c).Gap(6).Children(func() {
							ui.Text(c, label).FontSize(12).Bold().TextColor(color)
							ui.Text(c, a.tr("for this source/target")).FontSize(12).TextColor(t.TextMuted)
						})
					}
				}
				if ui.PrimaryButton(c, a.tr("Convert")).Clicked() {
					src := strings.TrimSpace(a.convSource)
					if src == "" {
						a.showToast(a.tr("Enter a source path"))
						return
					}
					if !a.requireTools(false, true) {
						return
					}
					mode := models.ConversionReencode
					if a.convKind == models.KindVideo {
						remux, checking, known := a.remuxDecision(src, a.convFormat)
						if checking || !known {
							a.showToast(a.tr("Still checking source compatibility"))
							return
						}
						if remux {
							mode = models.ConversionRemux
						}
					}
					settings := a.settingsDraftSnapshot()
					req := models.ConversionRequest{Source: src, TargetFormat: a.convFormat, Kind: a.convKind, Mode: mode, AudioBitrateKbps: a.convAudioBitrateKbps, KeepOriginal: settings.KeepOriginals}
					go func() {
						_, err := a.conv.EnqueueContext(context.Background(), req)
						a.updateOnMain(func() {
							if err != nil {
								a.showToast(a.tr("Could not queue conversion: ") + err.Error())
								return
							}
							a.showToast(a.tr("Queued conversion: ") + filepath.Base(req.Source))
						})
					}()
				}
			})
			ui.Column(c).Grow(5).Material(glass.Glass{Clarity: 0.2}).Border(1, t.Border).Padding(14).Radius(14).Gap(8).Children(func() {
				ui.Text(c, a.tr("Conversion queue")).FontSize(14).Bold()
				a.convJobsMu.Lock()
				jobs := append([]models.ConversionJobSnapshot(nil), a.convJobs...)
				a.convJobsMu.Unlock()
				sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.After(jobs[j].CreatedAt) })
				if len(jobs) == 0 {
					emptyState(c, appui.IconConverter, a.tr("No conversion jobs"), a.tr("Converted files will appear here."))
				} else {
					ui.List(c, &a.convListState, len(jobs), func(i int) {
						j := jobs[i]
						ui.Column(c).FillWidth().Background(t.Background).Border(1, t.Border).Padding(9).Radius(10).Gap(4).Children(func() {
							ui.Row(c).FillWidth().Children(func() {
								ui.Text(c, filepath.Base(j.Request.Source)).FontSize(12).Bold().Grow(1)
								statusPill(c, conversionDownloadState(j.State))
							})
							if j.State == models.ConvRunning {
								ui.Progress(c, j.Percent/100)
							}
							if j.State == models.ConvError {
								ui.Text(c, j.Error).FontSize(11).TextColor(t.Danger)
							}
							if j.State == models.ConvCompleted {
								ui.Text(c, filepath.Base(j.OutputPath)).FontSize(11).TextColor(t.TextMuted)
								path := j.OutputPath
								if ui.Button(c.Key("conv-show-"+j.ID), "Show in folder").Clicked() {
									go mygo.Shell.ShowItemInFolder(path)
								}
							}
							if !j.State.IsTerminal() && ui.Button(c.Key("cc-"+j.ID), "Cancel").Clicked() {
								a.conv.Cancel(j.ID)
							}
						})
					}).Grow(1).Gap(6)
				}
			})
		})
	})
}

func (a *App) remuxDecision(source, target string) (result, checking, known bool) {
	source = strings.TrimSpace(source)
	target = strings.TrimSpace(target)
	if source == "" || target == "" || a.ffmpeg == nil {
		return false, false, false
	}
	a.remuxMu.Lock()
	if a.remuxSource == source && a.remuxTarget == target {
		result, checking, known = a.remuxResult, a.remuxChecking, a.remuxKnown
		a.remuxMu.Unlock()
		return result, checking, known
	}
	a.remuxSource = source
	a.remuxTarget = target
	a.remuxChecking = true
	a.remuxKnown = false
	a.remuxMu.Unlock()

	go func() {
		result := a.ffmpeg.CanRemux(source, target)
		a.updateOnMain(func() {
			a.remuxMu.Lock()
			if a.remuxSource == source && a.remuxTarget == target {
				a.remuxResult = result
				a.remuxChecking = false
				a.remuxKnown = true
			}
			a.remuxMu.Unlock()
			a.invalidate()
		})
	}()
	return false, true, false
}

func (a *App) historyPage(c *ui.Context) {
	t := c.Theme()
	if a.history == nil {
		ui.Text(c, a.tr("History unavailable")).TextColor(t.Danger)
		return
	}
	if !a.historyLoaded {
		entries, err := a.history.List(200)
		if err != nil {
			ui.Text(c, err.Error()).TextColor(t.Danger)
			return
		}
		a.historyEntries = entries
		a.historyLoaded = true
	}
	filtered := make([]services.HistoryEntry, 0, len(a.historyEntries))
	query := strings.ToLower(strings.TrimSpace(a.historyQuery))
	for _, entry := range a.historyEntries {
		if a.historyFilter == "video" && strings.EqualFold(entry.Mode, string(models.ModeAudioOnly)) {
			continue
		}
		if a.historyFilter == "audio" && !strings.EqualFold(entry.Mode, string(models.ModeAudioOnly)) {
			continue
		}
		if a.historyFilter == "error" && !strings.EqualFold(entry.State, string(models.StateError)) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(entry.Title+" "+entry.URL), query) {
			continue
		}
		filtered = append(filtered, entry)
	}
	ui.Column(c).Grow(1).Gap(12).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, a.tr("history.count", map[string]any{"n": len(a.historyEntries)})).FontSize(12).TextColor(t.TextMuted)
			ui.Spacer(c).Grow(1)
			if ui.Button(c, a.tr("Refresh")).Clicked() {
				a.historyLoaded = false
			}
			if ui.Button(c, a.tr("Clear all")).Clicked() {
				if err := a.history.Clear(); err != nil {
					a.showToast(a.tr("Could not clear history: ") + err.Error())
				} else {
					a.historyLoaded = false
				}
			}
		})
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			ui.TextInput(c, &a.historyQuery).Placeholder(a.tr("Search title or URL…")).Grow(1)
			for _, filter := range []string{"all", "video", "audio", "error"} {
				filter := filter
				label := filter
				if filter != "all" {
					label = strings.Title(filter)
				}
				a.toggleBtn(c, "history-"+filter, label, a.historyFilter == filter || (a.historyFilter == "" && filter == "all"), func() {
					a.historyFilter = filter
				})
			}
		})
		if len(a.historyEntries) == 0 {
			emptyStateAction(c, appui.IconHistory, a.tr("history.empty.title"), a.tr("history.empty.detail"), a.tr("history.empty.action"), func() {
				a.page = PageDashboard
			})
			return
		}
		if len(filtered) == 0 {
			emptyState(c, appui.IconHistory, a.tr("No matches"), a.tr("Try another search or filter."))
			return
		}
		ui.List(c, &a.histListState, len(filtered), func(i int) {
			e := filtered[i]
			ui.Row(c).FillWidth().Material(glass.Glass{Clarity: 0.2}).Border(1, t.Border).Padding(10, 12).Radius(12).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(40, 40).Background(t.SurfaceHover).Radius(10).Center().Children(func() {
					if e.Mode == string(models.ModeAudioOnly) {
						ui.Icon(c, appui.IconMusic).FontSize(19).TextColor(t.Accent)
					} else {
						ui.Icon(c, appui.IconVideo).FontSize(19).TextColor(t.Accent)
					}
				})
				ui.Column(c).Grow(1).Gap(3).Children(func() {
					ui.Row(c).FillWidth().Children(func() {
						ui.Text(c, e.Title).FontSize(13).Bold().Grow(1)
						statusPill(c, historyDownloadState(e.State))
					})
					ui.Text(c, e.URL).FontSize(11).TextColor(t.TextMuted).MaxWidth(700)
					if e.OutputPath != "" {
						ui.Text(c, filepath.Base(e.OutputPath)).FontSize(11).TextColor(t.TextMuted)
					}
					if e.Error != "" {
						ui.Text(c, e.Error).FontSize(11).TextColor(t.Danger).MaxWidth(700)
					}
				})
				if e.OutputPath != "" {
					path := e.OutputPath
					if ui.Row(c.Key("hist-show-"+e.ID)).
						Background(ui.Color{}).Border(1, t.Border).Radius(8).Size(32, 32).Center().
						Children(func() {
							ui.Icon(c, appui.IconFolder).FontSize(16).TextColor(t.TextMuted)
						}).Clicked() {
						go mygo.Shell.ShowItemInFolder(path)
					}
				}
				if e.URL != "" {
					if ui.Row(c.Key("hist-redo-"+e.ID)).
						Background(ui.Color{}).Border(1, t.Border).Radius(8).Size(32, 32).Center().
						Children(func() {
							ui.Icon(c, appui.IconDownload).FontSize(16).TextColor(t.TextMuted)
						}).Clicked() {
						if !a.requireTools(true, false) {
							return
						}
						settings := a.settingsDraftSnapshot()
						req := historyDownloadRequest(e, settings.DownloadDir)
						a.page = PageDownloads
						a.showToast(a.tr("Queueing: ") + e.Title)
						if e.SourcePageURL != "" {
							go a.requeueHistorySource(e, req)
						} else {
							go func() {
								if err := restoreHistoryVideoQuality(&req, e, a.ffmpeg); err != nil {
									log.Printf("history: could not restore previous video quality for %q: %v", e.OutputPath, err)
									a.updateOnMain(func() {
										a.showToast(a.tr("Cannot restore previous quality: ") + err.Error())
									})
									return
								}
								_, err := a.manager.EnqueueContext(context.Background(), req)
								a.updateOnMain(func() {
									if err != nil {
										a.showToast(a.tr("Could not queue download: ") + err.Error())
										return
									}
									a.showToast(a.tr("Queued: ") + req.Title)
								})
							}()
						}
					}
				}
				if ui.Row(c.Key("hist-delete-"+e.ID)).
					Background(ui.Color{}).Border(1, t.Border).Radius(8).Size(32, 32).Center().
					Children(func() {
						ui.Icon(c, appui.IconDelete).FontSize(16).TextColor(t.Danger)
					}).Clicked() {
					if err := a.history.Delete(e.ID); err != nil {
						a.showToast(a.tr("Could not delete history entry: ") + err.Error())
					} else {
						a.historyLoaded = false
					}
				}
			})
		}).Grow(1).Gap(8)
	})
}

// requeueHistorySource re-resolves an extracted stream before retrying it,
// because the direct m3u8 URL stored at download time carries an auth key
// that expires.
func (a *App) requeueHistorySource(entry services.HistoryEntry, req models.DownloadRequest) {
	// The click handler already checked the tools on the UI thread; this
	// function runs in a worker and must not read UI state or change pages.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	res, err := services.ExtractSiteMedia(ctx, entry.SourcePageURL)
	if err != nil {
		a.updateOnMain(func() {
			a.extractInput = entry.SourcePageURL
			a.extractOpen = true
			a.page = PageDashboard
			a.showToast(a.tr("history.requeueExpired"))
			a.invalidate()
		})
		return
	}
	mediaURL := ""
	if title := strings.TrimSpace(entry.Title); title != "" {
		for _, video := range res.Videos {
			if strings.EqualFold(strings.TrimSpace(video.Title), title) {
				mediaURL = video.MediaURL
				break
			}
		}
	}
	if mediaURL == "" && entry.SourceIndex >= 0 && entry.SourceIndex < len(res.Videos) {
		mediaURL = res.Videos[entry.SourceIndex].MediaURL
	}
	if mediaURL == "" && len(res.Videos) == 1 {
		mediaURL = res.Videos[0].MediaURL
	}
	if mediaURL == "" {
		a.updateOnMain(func() {
			a.extractInput = entry.SourcePageURL
			a.extractOpen = true
			a.page = PageDashboard
			a.showToast(a.tr("history.requeueExpired"))
			a.invalidate()
		})
		return
	}
	req.URL = mediaURL
	if err := restoreHistoryVideoQuality(&req, entry, a.ffmpeg); err != nil {
		log.Printf("history: could not restore previous video quality for %q: %v", entry.OutputPath, err)
	}
	_, err = a.manager.EnqueueContext(context.Background(), req)
	a.updateOnMain(func() {
		if err != nil {
			a.showToast(a.tr("Could not queue download: ") + err.Error())
			return
		}
		a.showToast(a.tr("Queued: ") + req.Title)
	})
}

func (a *App) exportSettings(settings config.AppSettings) {
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: a.window(), Title: "Export settings", DefaultPath: "video_downloader_settings.json", Filters: []mygo.FileFilter{{Name: "JSON", Extensions: []string{"json"}}}})
	if err != nil || path == "" {
		return
	}
	data, err := json.MarshalIndent(exportableSettings(settings), "", "  ")
	if err != nil {
		a.updateOnMain(func() { a.showToast(a.tr("Could not export settings: ") + err.Error()) })
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		a.updateOnMain(func() { a.showToast(a.tr("Could not export settings: ") + err.Error()) })
		return
	}
	a.updateOnMain(func() { a.showToast(a.tr("Settings exported")) })
}

// exportableSettings removes local credentials before writing a settings file.
func exportableSettings(settings config.AppSettings) config.AppSettings {
	settings.ARYPlusAPIKey = ""
	return settings
}

// importedSettings preserves the locally stored ARY Plus key when an imported
// file does not carry one; exported files intentionally omit it.
func importedSettings(imported, existing config.AppSettings) config.AppSettings {
	if strings.TrimSpace(imported.ARYPlusAPIKey) == "" {
		imported.ARYPlusAPIKey = existing.ARYPlusAPIKey
	}
	return imported
}

func (a *App) importSettings() {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.window(), Title: "Import settings", Filters: []mygo.FileFilter{{Name: "JSON", Extensions: []string{"json"}}}})
	if err != nil || len(paths) == 0 {
		return
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		a.updateOnMain(func() { a.showToast(a.tr("Could not read settings: ") + err.Error()) })
		return
	}
	var settings config.AppSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		a.updateOnMain(func() { a.showToast(a.tr("Invalid settings file")) })
		return
	}
	// Exported settings never contain the ARY Plus key. Keep the user's local
	// key when importing a file that omits it.
	settings = importedSettings(settings, a.settingsSnapshot())
	var headerLines []string
	for key, value := range settings.CustomHeaders {
		headerLines = append(headerLines, key+": "+value)
	}
	sort.Strings(headerLines)
	validated, err := validateSettingsDraft(refinedSettingsDraft{
		settings:       settings,
		maxConcurrent:  strconv.Itoa(settings.MaxConcurrent),
		concurrentFrag: strconv.Itoa(settings.ConcurrentFragments),
		rateLimit:      strconv.Itoa(settings.RateLimitKbps),
		headers:        strings.Join(headerLines, "\n"),
		subtitleLangs:  strings.Join(settings.SubtitleLangs, ", "),
	})
	if err != nil {
		a.updateOnMain(func() { a.showToast(a.tr("Invalid settings: ") + err.Error()) })
		return
	}
	a.updateOnMain(func() {
		if validated.CustomHeaders == nil {
			validated.CustomHeaders = map[string]string{}
		}
		if a.applySettings(validated) {
			a.refreshToolChecks()
		}
		a.settingsHeaders = ""
		for key, value := range validated.CustomHeaders {
			a.settingsHeaders += key + ": " + value + "\n"
		}
		a.settingsSubtitleLangs = strings.Join(validated.SubtitleLangs, ", ")
		a.settingsRateLimit = strconv.Itoa(validated.RateLimitKbps)
		if err := a.settingsRepo.Save(validated); err != nil {
			a.showToast(a.tr("Could not save imported settings: ") + err.Error())
			return
		}
		if a.manager != nil {
			a.manager.SetMaxWorkers(validated.MaxConcurrent)
		}
		a.invalidate()
		a.showToast(a.tr("Settings imported"))
	})
}

func (a *App) pickConvertFile() {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: a.window(),
		Title:  a.tr("Choose media file"),
		Filters: []mygo.FileFilter{
			{Name: "Media", Extensions: []string{"mp4", "mkv", "webm", "avi", "mov", "flv", "ts", "m4v", "m4a", "mp3", "flac", "wav", "opus", "aac", "ogg", "wma"}},
			{Name: "All files", Extensions: []string{"*"}},
		},
	})
	if err != nil {
		mygo.Dialog.Error("Open failed", err.Error())
		return
	}
	if len(paths) == 0 {
		return // canceled
	}
	a.updateOnMain(func() {
		a.convSource = paths[0]
		a.invalidate()
	})
}

func (a *App) pickDownloadDir() {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:    a.window(),
		Title:     a.tr("Choose download folder"),
		Directory: true,
	})
	if err != nil {
		mygo.Dialog.Error("Open failed", err.Error())
		return
	}
	if len(paths) == 0 {
		return
	}
	a.updateOnMain(func() {
		a.ensureSettingsDraft()
		a.settingsDraft.DownloadDir = paths[0]
		a.publishSettingsDraft()
		a.invalidate()
	})
}

func formatDuration(sec float64) string {
	s := int(sec)
	h := s / 3600
	m := (s % 3600) / 60
	s = s % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func formatSpeed(bps float64) string {
	switch {
	case bps < 1024:
		return fmt.Sprintf("%.0f B/s", bps)
	case bps < 1024*1024:
		return fmt.Sprintf("%.1f KB/s", bps/1024)
	default:
		return fmt.Sprintf("%.1f MB/s", bps/(1024*1024))
	}
}

func formatBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.2f GB", float64(n)/(1024*1024*1024))
	}
}

func shortCodec(c string) string {
	c = strings.ToLower(c)
	for _, p := range []string{"avc1", "h264", "hev1", "hvc1", "vp09", "vp9", "av01", "av1", "mp4a", "opus"} {
		if strings.Contains(c, p) {
			return p
		}
	}
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

func boolPtr(value bool) *bool {
	return &value
}

// checkboxState returns a stable pointer for ui.Checkbox. The checkbox mutates
// the pointed-to value after the click event, so using a local bool here would
// lose the selection before the next render.
func checkboxState(states map[string]*bool, key string) *bool {
	if state, ok := states[key]; ok && state != nil {
		return state
	}
	state := boolPtr(false)
	states[key] = state
	return state
}

// languageNames is presentation-only. Subtitle selections continue to use
// the original yt-dlp language code as their map key and request value.
var languageNames = map[string]string{
	"af": "Afrikaans", "am": "Amharic", "ar": "Arabic", "az": "Azerbaijani",
	"be": "Belarusian", "bg": "Bulgarian", "bn": "Bengali", "bs": "Bosnian",
	"ca": "Catalan", "cs": "Czech", "cy": "Welsh", "da": "Danish",
	"de": "German", "el": "Greek", "en": "English", "eo": "Esperanto",
	"es": "Spanish", "et": "Estonian", "eu": "Basque", "fa": "Persian",
	"fi": "Finnish", "fil": "Filipino", "fr": "French", "ga": "Irish",
	"gl": "Galician", "gu": "Gujarati", "he": "Hebrew", "hi": "Hindi",
	"hr": "Croatian", "hu": "Hungarian", "hy": "Armenian", "id": "Indonesian",
	"is": "Icelandic", "it": "Italian", "ja": "Japanese", "jv": "Javanese",
	"ka": "Georgian", "kk": "Kazakh", "km": "Khmer", "kn": "Kannada",
	"ko": "Korean", "ky": "Kyrgyz", "lo": "Lao", "lt": "Lithuanian",
	"lv": "Latvian", "mk": "Macedonian", "ml": "Malayalam", "mn": "Mongolian",
	"mr": "Marathi", "ms": "Malay", "my": "Burmese", "nb": "Norwegian Bokmål",
	"ne": "Nepali", "nl": "Dutch", "nn": "Norwegian Nynorsk", "no": "Norwegian",
	"pa": "Punjabi", "pl": "Polish", "ps": "Pashto", "pt": "Portuguese",
	"ro": "Romanian", "ru": "Russian", "si": "Sinhala", "sk": "Slovak",
	"sl": "Slovenian", "sq": "Albanian", "sr": "Serbian", "sv": "Swedish",
	"sw": "Swahili", "ta": "Tamil", "te": "Telugu", "tg": "Tajik",
	"th": "Thai", "tk": "Turkmen", "tl": "Tagalog", "tr": "Turkish",
	"uk": "Ukrainian", "ur": "Urdu", "uz": "Uzbek", "vi": "Vietnamese",
	"zh": "Chinese", "zu": "Zulu",
	// yt-dlp and older metadata sometimes use these legacy ISO 639 aliases.
	"in": "Indonesian", "iw": "Hebrew", "jw": "Javanese",
	// Common 3-letter ISO 639-2/3 codes used by yt-dlp and media extractors.
	"ara": "Arabic", "ben": "Bengali", "ces": "Czech", "chi": "Chinese",
	"cze": "Czech", "dan": "Danish", "deu": "German", "dut": "Dutch",
	"ell": "Greek", "eng": "English", "fas": "Persian", "fin": "Finnish",
	"fra": "French", "fre": "French", "ger": "German", "gre": "Greek",
	"heb": "Hebrew", "hin": "Hindi", "ind": "Indonesian", "ita": "Italian",
	"jpn": "Japanese", "kor": "Korean", "nld": "Dutch", "nor": "Norwegian",
	"per": "Persian", "pol": "Polish", "por": "Portuguese", "ron": "Romanian",
	"rum": "Romanian", "rus": "Russian", "spa": "Spanish", "swe": "Swedish",
	"tha": "Thai", "tur": "Turkish", "ukr": "Ukrainian", "urd": "Urdu",
	"vie": "Vietnamese", "zho": "Chinese",
}

func languageDisplayName(language string) string {
	if language == "" {
		return ""
	}
	parts := strings.FieldsFunc(language, func(r rune) bool {
		return r == '-' || r == '_'
	})
	if len(parts) == 0 {
		return ""
	}
	base := strings.ToLower(parts[0])
	if base == "und" || base == "unknown" || base == "none" {
		return ""
	}
	if name, ok := languageNames[base]; ok {
		return name
	}
	return ""
}

func subtitleModeFromSettings(settings config.AppSettings) string {
	if len(settings.SubtitleLangs) == 0 {
		return subtitleModeNone
	}
	for _, lang := range settings.SubtitleLangs {
		if lang == "all" {
			return subtitleModeAll
		}
	}
	return subtitleModeCustom
}

func subtitleLanguages(m *models.MediaInfo) []string {
	if m == nil {
		return nil
	}
	// Return codes, not display labels: the codes are used unchanged when the
	// download request is converted to --sub-langs. Only creator-provided
	// subtitles are eligible; automatic captions are intentionally excluded.
	seen := make(map[string]bool, len(m.Subtitles))
	addAvailable := func(source map[string][]models.SubtitleTrack) {
		for lang, tracks := range source {
			lang = strings.TrimSpace(lang)
			cleanLower := strings.ToLower(lang)
			if lang == "" || cleanLower == "und" || cleanLower == "unknown" || cleanLower == "none" || strings.EqualFold(lang, "live_chat") || len(tracks) == 0 {
				continue
			}
			displayName := languageDisplayName(lang)
			if displayName == "" || displayName == "Unknown language" {
				continue
			}
			seen[lang] = true
		}
	}
	addAvailable(m.Subtitles)
	langs := make([]string, 0, len(seen))
	for lang := range seen {
		langs = append(langs, lang)
	}
	return sortSelectedSubtitleLangs(langs)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
