package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/fazi-gondal/video-downloader-mygo/internal/config"
	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
	"github.com/fazi-gondal/video-downloader-mygo/internal/services"
	appui "github.com/fazi-gondal/video-downloader-mygo/internal/ui"
)

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
)

type App struct {
	winMu              sync.RWMutex
	win                *mygo.Window
	pendingMu          sync.Mutex
	pendingUpdates     []func()
	settingsRepo       *config.SettingsRepository
	settingsMu         sync.RWMutex
	settings           config.AppSettings // worker-visible snapshot, protected by settingsMu
	settingsDraft      config.AppSettings // editable UI state, accessed on the main thread
	settingsDraftReady bool
	ffmpeg             *services.FFmpegService
	ytdlp              *services.YtDlpService
	manager            *services.DownloadManager
	conv               *services.ConversionService
	history            *services.HistoryService

	page string

	urlInput        string
	analyzing       bool
	analyzeError    string
	currentMedia    *models.MediaInfo
	currentPlaylist *models.PlaylistInfo
	selectedEntries map[int]bool

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
		return
	}
	a.settingsDraft = a.settingsSnapshot()
	a.settingsDraftReady = true
}

func (a *App) settingsDraftSnapshot() config.AppSettings {
	a.ensureSettingsDraft()
	return cloneAppSettings(a.settingsDraft)
}

func (a *App) publishSettingsDraft() {
	a.ensureSettingsDraft()
	a.setSettings(a.settingsDraft)
}

func (a *App) applySettings(settings config.AppSettings) {
	settings = cloneAppSettings(settings)
	a.setSettings(settings)
	a.settingsDraft = cloneAppSettings(settings)
	a.settingsDraftReady = true
}

func New() *App {
	repo := config.NewSettingsRepository()
	settings, err := repo.Load()
	if err != nil {
		log.Println("settings:", err)
	}
	ffmpeg := services.NewFFmpegService()
	a := &App{
		settingsRepo:         repo,
		settings:             cloneAppSettings(settings),
		settingsDraft:        cloneAppSettings(settings),
		settingsDraftReady:   true,
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
	go a.pumpDownloadEvents()
	go a.pumpConversionEvents()
	return a
}

func (a *App) SetWindow(w *mygo.Window) {
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

// Shutdown stops media workers before the application event loop exits. Both
// services make this operation idempotent so it is safe to call from the
// window lifecycle hook and from main's deferred cleanup.
func (a *App) Shutdown() {
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

func (a *App) pumpDownloadEvents() {
	for ev := range a.manager.Events() {
		if ev.Task == nil {
			continue
		}
		task := ev.Task.Snapshot()
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
				if a.playlistActive {
					if task.State == models.StateCompleted {
						a.playlistDone++
					} else {
						a.playlistFailed++
					}
					if a.playlistDone+a.playlistFailed >= a.playlistQueued {
						a.playlistActive = false
						a.showToast(fmt.Sprintf("Playlist done: %d ok, %d failed", a.playlistDone, a.playlistFailed))
					}
				}
				if task.State == models.StateCancelled {
					a.showToast("Download cancelled")
				} else if ev.Error == nil {
					name := filepath.Base(task.OutputPath)
					if name == "." || name == "" {
						name = task.Request.Title
					}
					a.showToast("Downloaded: " + name)
				} else if !a.playlistActive {
					a.showToast("Download failed")
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
					a.showToast("Conversion cancelled")
				} else if ev.Err == nil {
					a.showToast("Converted: " + filepath.Base(job.OutputPath))
				} else {
					a.showToast("Conversion failed")
				}
			}
			a.invalidate()
		})
	}
}

func (a *App) View(c *ui.Context) {
	a.ensureSettingsDraft()
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
	activeDownloads := 0
	for _, task := range a.tasks {
		if !task.State.IsTerminal() {
			activeDownloads++
		}
	}
	a.tasksMu.Unlock()
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		ui.Sidebar(c, &a.page, func() {
			ui.SidebarSection(c, "Library", &a.sidebarOpen[0], func() {
				ui.SidebarItem(c, PageDashboard, appui.IconHome, "Home")
				ui.SidebarItem(c, PageDownloads, appui.IconDownload, "Downloads").Children(func() {
					if activeDownloads > 0 {
						ui.Badge(c, fmt.Sprintf("%d", activeDownloads))
					}
				})
				ui.SidebarItem(c, PageConverter, appui.IconConverter, "Converter")
				ui.SidebarItem(c, PageHistory, appui.IconHistory, "History")
			})
			ui.SidebarSection(c, "App", &a.sidebarOpen[1], func() {
				ui.SidebarItem(c, PageSettings, appui.IconSettings, "Settings")
				ui.SidebarItem(c, PageAbout, appui.IconAbout, "About")
			})
		}).Width(212)
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
		PageDashboard: "Home", PageDownloads: "Downloads", PageConverter: "Converter",
		PageHistory: "History", PageSettings: "Settings", PageAbout: "About",
		PageConfig: "Download options",
	}
	ui.Row(c).Height(44).FillWidth().Padding(0, 16).AlignItems(ui.Center).Background(t.Surface).Children(func() {
		ui.Text(c, titles[a.page]).FontSize(15).Bold()
		ui.Spacer(c).Grow(1)
		if a.playlistActive {
			done := a.playlistDone + a.playlistFailed
			ui.Text(c, fmt.Sprintf("Playlist %d/%d", done, a.playlistQueued)).FontSize(12).TextColor(t.TextMuted)
			ui.Progress(c, float64(done)/float64(maxInt(a.playlistQueued, 1))).Width(120)
		}
		a.tasksMu.Lock()
		n := 0
		for _, task := range a.tasks {
			if !task.State.IsTerminal() {
				n++
			}
		}
		a.tasksMu.Unlock()
		if n > 0 {
			ui.Text(c, fmt.Sprintf("%d active", n)).FontSize(12).TextColor(t.Accent)
		}
	})
}

func (a *App) toastBar(c *ui.Context) {
	if a.toastMsg == "" || time.Now().After(a.toastUntil) {
		return
	}
	t := c.Theme()
	ui.Row(c).FillWidth().Justify(ui.Center).Padding(0, 0, 16, 0).Children(func() {
		ui.Row(c).Background(t.Surface).Padding(8, 14).Radius(8).Children(func() {
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
		if a.analyzeError != "" {
			ui.Row(c).FillWidth().Background(t.Surface).Border(1, t.Danger).Radius(10).Padding(10, 12).Children(func() {
				ui.Text(c, a.analyzeError).FontSize(13).TextColor(t.Danger).Grow(1)
			})
		}
		if a.currentMedia != nil {
			a.mediaCard(c)
			ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
				if ui.PrimaryButton(c, "Continue to download options").Clicked() {
					a.page = PageConfig
				}
			})
		} else if a.currentPlaylist != nil {
			a.playlistPanel(c)
		} else if a.analyzing {
			ui.Column(c).FillWidth().Padding(42, 0).Center().Gap(10).Children(func() {
				ui.Spinner(c)
				ui.Text(c, "Analyzing media…").FontSize(13).TextColor(t.TextMuted)
			})
		}
	})
}

func (a *App) dashboardHero(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).FillWidth().Grow(1).Center().Gap(18).Padding(28, 20).Children(func() {
		ui.Row(c).Justify(ui.Center).Children(func() {
			ui.Text(c, "Extract. ").FontSize(29).Bold()
			ui.Text(c, "Convert. ").FontSize(29).Bold().TextColor(t.TextMuted)
			ui.Text(c, "Master.").FontSize(29).Bold().TextColor(t.Accent)
		})
		ui.Text(c, "Download, organize, and convert media from your favorite platforms.").FontSize(14).TextColor(t.TextMuted).MaxWidth(560)
		a.dashboardSearch(c)
		ui.Column(c).Center().Gap(8).Children(func() {
			ui.Text(c, "SUPPORTED SERVICES").FontSize(10).Bold().TextColor(t.TextMuted)
			ui.Row(c).Justify(ui.Center).Gap(18).Children(func() {
				for i, service := range []string{"YouTube", "TikTok", "Instagram", "X", "ARY Plus"} {
					ui.Row(c).Gap(5).Children(func() {
						icon := appui.IconVideo
						if i == 1 {
							icon = appui.IconMusic
						}
						ui.Icon(c, icon).FontSize(14).TextColor(t.TextMuted)
						ui.Text(c, service).FontSize(11).TextColor(t.TextMuted)
					})
				}
			})
		})
	})
}

func (a *App) dashboardSearch(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).FillWidth().MaxWidth(720).Background(t.Surface).Border(1, t.Border).Radius(12).Padding(6, 8).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, appui.IconLink).FontSize(18).TextColor(t.TextMuted).Width(20)
		ui.TextInput(c, &a.urlInput).Placeholder("Paste video or playlist URL…").Grow(1)
		btn := ui.PrimaryButton(c, "Analyze")
		if a.analyzing {
			btn.Disabled(true)
		}
		if btn.Clicked() && !a.analyzing {
			a.analyzing = true
			a.analyzeError = ""
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
			a.invalidate()
			go a.doAnalyze(strings.TrimSpace(a.urlInput))
		}
	})
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
	ui.Row(c).FillWidth().Background(t.Surface).Border(1, t.Border).Padding(14, 16).Radius(14).Gap(14).AlignItems(ui.Center).Children(func() {
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
		})
		ui.Column(c).Gap(5).AlignItems(ui.End).Children(func() {
			if count := len(highQualityOpusAudioTracks(m.AudioTracks)); count > 0 {
				if count == 1 {
					ui.Text(c, "1 audio track").FontSize(11).TextColor(t.Accent)
				} else {
					ui.Text(c, fmt.Sprintf("%d audio tracks", count)).FontSize(11).TextColor(t.Accent)
				}
			}
			if subCount := len(subtitleLanguages(m)); subCount > 0 {
				if subCount == 1 {
					ui.Text(c, "1 subtitle language").FontSize(11).TextColor(t.Accent)
				} else {
					ui.Text(c, fmt.Sprintf("%d subtitle languages", subCount)).FontSize(11).TextColor(t.Accent)
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
		ui.Collapsible(c, "Formats (optional)", &a.formatsOpen, func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if a.formatsLoading {
					ui.Spinner(c)
					ui.Text(c, "Loading formats…").FontSize(12).TextColor(t.TextMuted)
				} else if ui.Button(c, "Load available formats").Clicked() {
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

	ui.Collapsible(c, "Formats (optional)", &a.formatsOpen, func() {
		ui.Column(c).FillWidth().Gap(8).Padding(4, 0, 0, 0).Children(func() {
			ui.Text(c, "Leave Auto unless you need a specific stream. Video+Audio always merges audio.").FontSize(11).TextColor(t.TextMuted)

			ui.Text(c, "Video stream").FontSize(12).TextColor(t.TextMuted)
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

			ui.Text(c, "Audio stream").FontSize(12).TextColor(t.TextMuted)
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
				if ui.Button(c, "Reset to Auto").Clicked() {
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
	ui.Column(c).FillWidth().Background(t.Surface).Padding(14).Radius(10).Gap(10).Children(func() {
		ui.Text(c, "Download options").FontSize(13).Bold()
		ui.Row(c).Gap(8).Children(func() {
			a.modeBtn(c, models.ModeVideoAudio, "Video + Audio")
			a.modeBtn(c, models.ModeAudioOnly, "Audio only")
			a.modeBtn(c, models.ModeVideoOnly, "Video only")
		})
		if a.mode != models.ModeAudioOnly {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Max quality").FontSize(12).TextColor(t.TextMuted).Width(90)
				qLabels := []string{"Best", "2160p", "1440p", "1080p", "720p", "480p", "360p"}
				qVals := []int{0, 2160, 1440, 1080, 720, 480, 360}
				curQ := "Best"
				for i, h := range qVals {
					if a.maxHeight == h {
						curQ = qLabels[i]
						break
					}
				}
				if ui.Select(c, &curQ, qLabels).Changed() {
					for i, lab := range qLabels {
						if lab == curQ {
							a.maxHeight = qVals[i]
							// resolution preset must not pin a video-only format id
							a.selectedVideoFormat = ""
							a.selectedVideoLabel = "Auto (best for resolution)"
							break
						}
					}
				}
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Container").FontSize(12).TextColor(t.TextMuted).Width(90)
				ct := a.container
				containerLocked := a.containerSelectionLocked()
				if ui.Select(c, &ct, []string{"mp4", "mkv", "webm"}).Disabled(containerLocked).Changed() && !containerLocked {
					a.container = ct
					a.preferredContainer = ct
				}
			})
		}
		if a.mode == models.ModeAudioOnly {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Format").FontSize(12).TextColor(t.TextMuted).Width(90)
				af := a.audioFormat
				if ui.Select(c, &af, []string{"mp3", "m4a", "opus", "flac", "wav"}).Changed() {
					a.audioFormat = af
				}
			})
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
		if estimate := a.estimatedSizeText(); estimate != "" {
			ui.Text(c, estimate).FontSize(11).TextColor(t.TextMuted).Ellipsis("…")
		}
		if ui.PrimaryButton(c, "Start download").Clicked() {
			if a.currentMedia != nil {
				a.startDownload()
			} else if a.currentPlaylist != nil {
				a.startPlaylistDownloads()
			}
		}
	})
}

func (a *App) audioTracksControls(c *ui.Context) {
	if a.currentMedia == nil {
		return
	}
	tracks := highQualityOpusAudioTracks(a.currentMedia.AudioTracks)
	if len(tracks) == 0 {
		return
	}
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Text(c, "Audio tracks").FontSize(12).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			a.selectionModeBtn(c, "audio-tracks", &a.audioTracksMode, audioTracksNone, "Default")
			a.selectionModeBtn(c, "audio-tracks", &a.audioTracksMode, audioTracksAll, "All")
			a.selectionModeBtn(c, "audio-tracks", &a.audioTracksMode, audioTracksCustom, "Custom")
		})
		if a.audioTracksMode == audioTracksCustom {
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
		}
	})
}

func highQualityOpusAudioTracks(tracks []models.AudioTrack) []models.AudioTrack {
	if len(tracks) == 0 {
		return nil
	}

	bestByLang := make(map[string]models.AudioTrack)
	for _, tr := range tracks {
		langKey := strings.ToLower(strings.TrimSpace(tr.Language))
		if langKey == "und" || langKey == "unknown" || langKey == "none" {
			langKey = ""
		}
		if langKey == "" {
			langKey = tr.ID
		}
		if tr.IsOriginalTrack() {
			langKey += "\x00orig"
		}

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
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Text(c, "Subtitles").FontSize(12).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			a.selectionModeBtn(c, "subtitles", &a.subtitleMode, subtitleModeNone, "None")
			a.selectionModeBtn(c, "subtitles", &a.subtitleMode, subtitleModeAll, "All")
			a.selectionModeBtn(c, "subtitles", &a.subtitleMode, subtitleModeCustom, "Custom")
		})
		if a.subtitleMode == subtitleModeCustom {
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
		}
	})
}

func (a *App) selectionModeBtn(c *ui.Context, key string, selected *string, value, label string) {
	a.toggleBtn(c, key+"-"+value, label, *selected == value, func() {
		*selected = value
	})
}

func (a *App) modeBtn(c *ui.Context, mode models.DownloadMode, label string) {
	a.toggleBtn(c, "mode-"+string(mode), label, a.mode == mode, func() {
		a.mode = mode
	})
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
		ui.Text(c, fmt.Sprintf("%d videos · %s", len(p.Entries), p.Uploader)).FontSize(12).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, "Select all").Clicked() {
				for i := range p.Entries {
					a.selectedEntries[i] = true
				}
			}
			if ui.Button(c, "Select none").Clicked() {
				a.selectedEntries = map[int]bool{}
			}
			n := 0
			for _, v := range a.selectedEntries {
				if v {
					n++
				}
			}
			ui.Text(c, fmt.Sprintf("%d selected", n)).FontSize(12).TextColor(t.TextMuted)
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
		if ui.PrimaryButton(c, "Download selected").Clicked() {
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
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).FillWidth().Gap(12).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				if ui.Button(c, "← Back").Clicked() {
					a.page = PageDashboard
				}
				ui.Text(c, "Download configuration").FontSize(18).Bold()
			})
			if a.currentMedia != nil {
				a.mediaCard(c)
			}
			a.configPanel(c)
			a.formatPicker(c)
		})
	})
}

func (a *App) doAnalyze(url string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result, err := a.ytdlp.Analyze(ctx, url)
	if err != nil {
		a.updateOnMain(func() {
			a.analyzing = false
			a.analyzeError = err.Error()
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
	req := a.buildRequest(a.currentMedia.WebpageURL, a.currentMedia.Title, a.currentMedia.Thumbnail, "", 0)
	if task := a.manager.Enqueue(req); task == nil {
		a.showToast("Could not queue download: download manager is shut down")
		return
	}
	a.page = PageDownloads
	a.showToast("Queued: " + a.currentMedia.Title)
}

func (a *App) startPlaylistDownloads() {
	if a.currentPlaylist == nil {
		return
	}
	n := 0
	for i, ent := range a.currentPlaylist.Entries {
		if !a.selectedEntries[i] || ent.URL == "" {
			continue
		}
		req := a.buildRequest(ent.URL, ent.Title, ent.Thumbnail, a.currentPlaylist.Title, ent.Index)
		if a.manager.Enqueue(req) != nil {
			n++
		}
	}
	if n == 0 {
		a.showToast("No videos selected")
		return
	}
	a.playlistQueued = n
	a.playlistDone = 0
	a.playlistFailed = 0
	a.playlistActive = true
	a.page = PageDownloads
	a.showToast(fmt.Sprintf("Queued %d videos", n))
}

func (a *App) containerSelectionLocked() bool {
	if a.audioTracksMode == audioTracksAll {
		return true
	}
	if a.audioTracksMode == audioTracksCustom && countSelected(a.selectedAudio) > 1 {
		return true
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
	return "Estimated final size: " + marker + formatBytes(size)
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
					addFormat(bestAudioFormatForRequest(m.Formats, a.container, settings.PreferVP9Video))
				}
			default:
				format := findFormat(m.Formats, a.selectedAudioFormat)
				if format == nil {
					format = bestAudioFormatForRequest(m.Formats, a.container, settings.PreferVP9Video)
				}
				addFormat(format)
			}
		}
	}
	if a.mode == models.ModeAudioOnly {
		format := findFormat(m.Formats, a.selectedAudioFormat)
		if format == nil {
			format = bestAudioFormatForRequest(m.Formats, a.container, settings.PreferVP9Video)
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

	container = strings.ToLower(strings.TrimSpace(container))
	if container == "mp4" && !preferVP9 {
		mp4 := filterFormats(candidates, func(format *models.FormatInfo) bool {
			return strings.EqualFold(format.Ext, "mp4")
		})
		if len(mp4) > 0 {
			candidates = mp4
		}
	}
	if preferVP9 {
		vp9 := filterFormats(candidates, func(format *models.FormatInfo) bool {
			return strings.Contains(strings.ToLower(format.VCodec), "vp9")
		})
		if len(vp9) > 0 {
			candidates = vp9
		}
	}

	best := candidates[0]
	for _, format := range candidates[1:] {
		if formatHeight(*format) > formatHeight(*best) ||
			(formatHeight(*format) == formatHeight(*best) && format.TBR > best.TBR) {
			best = format
		}
	}
	return best
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

func bestAudioFormatForRequest(formats []models.FormatInfo, container string, preferVP9 bool) *models.FormatInfo {
	candidates := make([]*models.FormatInfo, 0, len(formats))
	for i := range formats {
		if formatIsAudioOnly(&formats[i]) {
			candidates = append(candidates, &formats[i])
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	container = strings.ToLower(strings.TrimSpace(container))
	preferredExt := ""
	preferredCodec := ""
	if container == "mp4" && !preferVP9 {
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
		if strings.ToLower(nameI) != strings.ToLower(nameJ) {
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
		if strings.ToLower(nameI) != strings.ToLower(nameJ) {
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
		emptyState(c, appui.IconDownload, "No downloads yet", "Your downloaded videos and audio will appear here.")
		return
	}
	active, queued, finished := 0, 0, 0
	for _, task := range tasks {
		switch task.State {
		case models.StatePending:
			queued++
		case models.StateCompleted, models.StateError, models.StateCancelled:
			finished++
		default:
			active++
		}
	}
	ui.Column(c).Grow(1).Gap(12).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, "Downloads").FontSize(20).Bold()
				ui.Text(c, fmt.Sprintf("%d active · %d queued · %d finished", active, queued, finished)).FontSize(12).TextColor(t.TextMuted)
			})
			ui.Spacer(c).Grow(1)
			if finished > 0 && ui.Button(c, "Clear finished").Clicked() {
				a.manager.ClearFinished()
				a.invalidate()
			}
		})
		ui.List(c, &a.dlListState, len(tasks), func(i int) {
			task := tasks[i]
			ui.Row(c).FillWidth().Background(t.Surface).Border(1, t.Border).Padding(10, 12).Radius(12).Gap(12).AlignItems(ui.Center).Children(func() {
				a.thumbnailElement(c, task.Request.ThumbnailURL, 112, 63, downloadFallbackIcon(task.Request.Mode))
				ui.Column(c).Grow(1).Gap(5).Children(func() {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
						ui.Text(c, task.Request.Title).FontSize(13).Bold().Grow(1).MaxWidth(680)
						statusPill(c, task.State)
					})
					if task.State == models.StatePreparing || task.State == models.StateDownloading || task.State == models.StateProcessing {
						pct := float64(-1)
						if fraction, ok := taskProgressFraction(task.State, task.Progress); ok {
							pct = fraction
						}
						ui.Progress(c, pct)
						ui.Row(c).FillWidth().Children(func() {
							ui.Text(c, downloadMetrics(task.Progress)).FontSize(11).TextColor(t.TextMuted)
							ui.Spacer(c).Grow(1)
						})
					}
					if task.State == models.StateError && task.Error != "" {
						ui.Text(c, task.Error).FontSize(11).TextColor(t.Danger).MaxWidth(720)
					}
					if task.ErrorDetail != "" {
						ui.Text(c, task.ErrorDetail).FontSize(11).TextColor(t.Warning).MaxWidth(720)
					}
					ui.Row(c).Gap(6).Children(func() {
						if !task.State.IsTerminal() {
							if ui.Button(c.Key("cancel-"+task.ID), "Cancel").Clicked() {
								a.manager.Cancel(task.ID)
							}
						}
						if task.State == models.StateError || task.State == models.StateCancelled {
							if ui.Button(c.Key("retry-"+task.ID), "Retry").Clicked() {
								a.manager.Retry(task.ID)
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

func emptyState(c *ui.Context, icon *ui.SVG, title, detail string) {
	t := c.Theme()
	ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
		ui.Box(c).Size(56, 56).Background(t.Surface).Border(1, t.Border).Radius(18).Center().Children(func() {
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

func downloadMetrics(p models.ProgressInfo) string {
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
		parts = append(parts, formatBytes(p.DownloadedBytes)+" / size unknown")
	}
	if p.SpeedBps > 0 {
		parts = append(parts, formatSpeed(p.SpeedBps))
	} else {
		parts = append(parts, "speed calculating")
	}
	if p.ETA > 0 {
		parts = append(parts, "ETA "+formatDuration(p.ETA.Seconds()))
	} else {
		parts = append(parts, "ETA calculating")
	}
	return strings.Join(parts, " · ")
}

func (a *App) converterPage(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).Gap(12).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, "Media Converter").FontSize(20).Bold()
				ui.Text(c, "Remux or re-encode your local media with FFmpeg.").FontSize(12).TextColor(t.TextMuted)
			})
		})
		if !a.ffmpeg.Available() {
			ui.Row(c).Background(t.Surface).Border(1, t.Danger).Radius(12).Padding(12).Children(func() {
				ui.Text(c, "FFmpeg not found on PATH. Install FFmpeg to enable conversion.").FontSize(13).TextColor(t.Danger)
			})
			return
		}
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Grow(7).Background(t.Surface).Border(1, t.Border).Padding(14).Radius(14).Gap(10).Children(func() {
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
					ui.Text(c, "Drop a media file here").FontSize(13).TextColor(t.TextMuted)
					ui.Text(c, "or use Browse").FontSize(11).TextColor(t.TextMuted)
				})
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "Source").FontSize(12).TextColor(t.TextMuted).Width(90)
					ui.TextInput(c, &a.convSource).Placeholder("/path/to/file.mp4").Grow(1)
					if ui.Button(c, "Browse…").Clicked() {
						go a.pickConvertFile()
					}
				})
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, "Kind").FontSize(12).TextColor(t.TextMuted).Width(90)
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
					ui.Text(c, "Target").FontSize(12).TextColor(t.TextMuted).Width(90)
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
						ui.Text(c, "Audio bitrate").FontSize(12).TextColor(t.TextMuted).Width(90)
						for _, bitrate := range []int{128, 192, 256, 320} {
							bitrate := bitrate
							a.toggleBtn(c, fmt.Sprintf("conv-bitrate-%d", bitrate), fmt.Sprintf("%d kbps", bitrate), a.convAudioBitrateKbps == bitrate, func() {
								a.convAudioBitrateKbps = bitrate
							})
						}
					})
					if a.convFormat == "flac" || a.convFormat == "wav" {
						ui.Text(c, "Lossless output ignores bitrate selection.").FontSize(11).TextColor(t.TextMuted)
					}
				}
				if strings.TrimSpace(a.convSource) != "" {
					if a.convKind == models.KindVideo {
						remux, checking, known := a.remuxDecision(a.convSource, a.convFormat)
						label, color := "Checking compatibility…", t.TextMuted
						if known && remux {
							label, color = "Lossless remux", t.Success
						} else if known && !remux {
							label, color = "Re-encode", t.Warning
						} else if !checking {
							label, color = "Re-encode", t.Warning
						}
						ui.Row(c).Gap(6).Children(func() {
							ui.Text(c, label).FontSize(12).Bold().TextColor(color)
							ui.Text(c, "for this source/target").FontSize(12).TextColor(t.TextMuted)
						})
					}
				}
				if ui.PrimaryButton(c, "Convert").Clicked() {
					src := strings.TrimSpace(a.convSource)
					if src == "" {
						a.showToast("Enter a source path")
						return
					}
					mode := models.ConversionReencode
					if a.convKind == models.KindVideo {
						remux, checking, known := a.remuxDecision(src, a.convFormat)
						if checking || !known {
							a.showToast("Still checking source compatibility")
							return
						}
						if remux {
							mode = models.ConversionRemux
						}
					}
					settings := a.settingsDraftSnapshot()
					if a.conv.Enqueue(models.ConversionRequest{Source: src, TargetFormat: a.convFormat, Kind: a.convKind, Mode: mode, AudioBitrateKbps: a.convAudioBitrateKbps, KeepOriginal: settings.KeepOriginals}) == nil {
						a.showToast("Could not queue conversion: conversion service is shut down")
					}
				}
			})
			ui.Column(c).Grow(5).Background(t.Surface).Border(1, t.Border).Padding(14).Radius(14).Gap(8).Children(func() {
				ui.Text(c, "Conversion queue").FontSize(14).Bold()
				a.convJobsMu.Lock()
				jobs := append([]models.ConversionJobSnapshot(nil), a.convJobs...)
				a.convJobsMu.Unlock()
				sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.After(jobs[j].CreatedAt) })
				if len(jobs) == 0 {
					emptyState(c, appui.IconConverter, "No conversion jobs", "Converted files will appear here.")
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
		ui.Text(c, "History unavailable").TextColor(t.Danger)
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
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, "History").FontSize(20).Bold()
				ui.Text(c, fmt.Sprintf("%d saved downloads", len(a.historyEntries))).FontSize(12).TextColor(t.TextMuted)
			})
			ui.Spacer(c).Grow(1)
			if ui.Button(c, "Refresh").Clicked() {
				a.historyLoaded = false
			}
			if ui.Button(c, "Clear all").Clicked() {
				if err := a.history.Clear(); err != nil {
					a.showToast("Could not clear history: " + err.Error())
				} else {
					a.historyLoaded = false
				}
			}
		})
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			ui.TextInput(c, &a.historyQuery).Placeholder("Search title or URL…").Grow(1)
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
			emptyState(c, appui.IconHistory, "No history yet", "Completed downloads will be saved here.")
			return
		}
		if len(filtered) == 0 {
			emptyState(c, appui.IconHistory, "No matches", "Try another search or filter.")
			return
		}
		ui.List(c, &a.histListState, len(filtered), func(i int) {
			e := filtered[i]
			ui.Row(c).FillWidth().Background(t.Surface).Border(1, t.Border).Padding(10, 12).Radius(12).Gap(10).AlignItems(ui.Center).Children(func() {
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
					if ui.Button(c.Key("hist-show-"+e.ID), "Open folder").Clicked() {
						go mygo.Shell.ShowItemInFolder(path)
					}
				}
				if e.URL != "" && ui.Button(c.Key("hist-redo-"+e.ID), "Download again").Clicked() {
					settings := a.settingsDraftSnapshot()
					if a.manager.Enqueue(models.DownloadRequest{URL: e.URL, Title: e.Title, Mode: historyMode(e.Mode), OutputDir: settings.DownloadDir, Container: e.Container, AudioFormat: e.AudioFmt}) == nil {
						a.showToast("Could not queue download: download manager is shut down")
						return
					}
					a.page = PageDownloads
					a.showToast("Queued: " + e.Title)
				}
				if ui.Button(c.Key("hist-delete-"+e.ID), "Delete").Clicked() {
					if err := a.history.Delete(e.ID); err != nil {
						a.showToast("Could not delete history entry: " + err.Error())
					} else {
						a.historyLoaded = false
					}
				}
			})
		}).Grow(1).Gap(8)
	})
}

func (a *App) exportSettings(settings config.AppSettings) {
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: a.window(), Title: "Export settings", DefaultPath: "video_downloader_settings.json", Filters: []mygo.FileFilter{{Name: "JSON", Extensions: []string{"json"}}}})
	if err != nil || path == "" {
		return
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		a.updateOnMain(func() { a.showToast("Could not export settings: " + err.Error()) })
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		a.updateOnMain(func() { a.showToast("Could not export settings: " + err.Error()) })
		return
	}
	a.updateOnMain(func() { a.showToast("Settings exported") })
}

func (a *App) importSettings() {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.window(), Title: "Import settings", Filters: []mygo.FileFilter{{Name: "JSON", Extensions: []string{"json"}}}})
	if err != nil || len(paths) == 0 {
		return
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		a.updateOnMain(func() { a.showToast("Could not read settings: " + err.Error()) })
		return
	}
	var settings config.AppSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		a.updateOnMain(func() { a.showToast("Invalid settings file") })
		return
	}
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
		a.updateOnMain(func() { a.showToast("Invalid settings: " + err.Error()) })
		return
	}
	a.updateOnMain(func() {
		if validated.CustomHeaders == nil {
			validated.CustomHeaders = map[string]string{}
		}
		a.applySettings(validated)
		a.settingsHeaders = ""
		for key, value := range validated.CustomHeaders {
			a.settingsHeaders += key + ": " + value + "\n"
		}
		a.settingsSubtitleLangs = strings.Join(validated.SubtitleLangs, ", ")
		a.settingsRateLimit = strconv.Itoa(validated.RateLimitKbps)
		if err := a.settingsRepo.Save(validated); err != nil {
			a.showToast("Could not save imported settings: " + err.Error())
			return
		}
		if a.manager != nil {
			a.manager.SetMaxWorkers(validated.MaxConcurrent)
		}
		a.invalidate()
		a.showToast("Settings imported")
	})
}

func (a *App) pickConvertFile() {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: a.window(),
		Title:  "Choose media file",
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
		Title:     "Choose download folder",
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
