package app

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/fazi-gondal/video-downloader-mygo/internal/config"
	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
	"github.com/fazi-gondal/video-downloader-mygo/internal/services"
)

const (
	PageDashboard = "dashboard"
	PageDownloads = "downloads"
	PageConverter = "converter"
	PageHistory   = "history"
	PageSettings  = "settings"
	PageAbout     = "about"
)

type App struct {
	win          *mygo.Window
	settingsRepo *config.SettingsRepository
	settings     config.AppSettings
	ffmpeg       *services.FFmpegService
	ytdlp        *services.YtDlpService
	manager      *services.DownloadManager
	conv         *services.ConversionService
	history      *services.HistoryService

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
	audioFormat         string
	writeSubs           bool
	multiAudio          bool
	selectedAudio       map[string]bool
	selectedVideoFormat string
	selectedAudioFormat string
	selectedVideoLabel  string // shown in Select
	selectedAudioLabel  string
	formatsOpen         bool // Collapsible: advanced format IDs

	videoFmtLabels []string
	videoFmtIDs    []string
	audioFmtLabels []string
	audioFmtIDs    []string

	playlistQueued, playlistDone, playlistFailed int
	playlistActive                               bool

	tasksMu sync.Mutex
	tasks   []*models.DownloadTask

	convSource string
	convFormat string
	convKind   models.MediaKind
	convJobs   []*models.ConversionJob
	convJobsMu sync.Mutex

	historyEntries []services.HistoryEntry
	historyLoaded  bool

	dlListState, plListState, fmtListState, histListState, convListState ui.ListState

	toastMsg    string
	toastUntil  time.Time
	sidebarOpen [2]bool
}

func New() *App {
	repo := config.NewSettingsRepository()
	settings := repo.Load()
	ffmpeg := services.NewFFmpegService()
	a := &App{
		settingsRepo:       repo,
		settings:           settings,
		ffmpeg:             ffmpeg,
		page:               PageDashboard,
		mode:               models.ModeVideoAudio,
		maxHeight:          1080,
		container:          "mp4",
		audioFormat:        "mp3",
		convFormat:         "mp3",
		convKind:           models.KindAudio,
		selectedEntries:    map[int]bool{},
		selectedAudio:      map[string]bool{},
		writeSubs:          settings.WriteSubtitles,
		multiAudio:         settings.MultiAudio,
		sidebarOpen:        [2]bool{true, true},
		formatsOpen:        false,
		selectedVideoLabel: "Auto (best for resolution)",
		selectedAudioLabel: "Auto (Opus / best)",
	}
	a.ytdlp = services.NewYtDlpService(ffmpeg.Path(), func() config.AppSettings { return a.settings })
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

func (a *App) SetWindow(w *mygo.Window) { a.win = w }
func (a *App) invalidate() {
	if a.win != nil {
		a.win.Invalidate()
	}
}
func (a *App) showToast(msg string) {
	a.toastMsg = msg
	a.toastUntil = time.Now().Add(3 * time.Second)
	a.invalidate()
}

func (a *App) pumpDownloadEvents() {
	for ev := range a.manager.Events() {
		a.tasksMu.Lock()
		a.tasks = a.manager.All()
		a.tasksMu.Unlock()
		if ev.Kind == services.EventTaskFinished {
			if a.history != nil {
				_ = a.history.Record(ev.Task)
				a.historyLoaded = false
			}
			if a.playlistActive {
				if ev.Task.State == models.StateCompleted {
					a.playlistDone++
				} else {
					a.playlistFailed++
				}
				if a.playlistDone+a.playlistFailed >= a.playlistQueued {
					a.playlistActive = false
					a.showToast(fmt.Sprintf("Playlist done: %d ok, %d failed", a.playlistDone, a.playlistFailed))
				}
			}
			if ev.Task.State == models.StateCancelled {
				a.showToast("Download cancelled")
			} else if ev.Error == nil {
				a.showToast("Downloaded: " + ev.Task.Filename())
			} else if !a.playlistActive {
				a.showToast("Download failed")
			}
		}
		a.invalidate()
	}
}

func (a *App) pumpConversionEvents() {
	for ev := range a.conv.Events() {
		a.convJobsMu.Lock()
		a.convJobs = a.conv.All()
		a.convJobsMu.Unlock()
		if ev.Kind == services.ConvEventFinished {
			if ev.Job.State == models.ConvCancelled {
				a.showToast("Conversion cancelled")
			} else if ev.Err == nil {
				a.showToast("Converted: " + filepath.Base(ev.Job.OutputPath))
			} else {
				a.showToast("Conversion failed")
			}
		}
		a.invalidate()
	}
}

func (a *App) View(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		ui.Sidebar(c, &a.page, func() {
			ui.SidebarSection(c, "Library", &a.sidebarOpen[0], func() {
				ui.SidebarItem(c, PageDashboard, nil, "Dashboard")
				ui.SidebarItem(c, PageDownloads, nil, "Downloads")
				ui.SidebarItem(c, PageConverter, nil, "Converter")
				ui.SidebarItem(c, PageHistory, nil, "History")
			})
			ui.SidebarSection(c, "App", &a.sidebarOpen[1], func() {
				ui.SidebarItem(c, PageSettings, nil, "Settings")
				ui.SidebarItem(c, PageAbout, nil, "About")
			})
		}).Width(200)
		ui.Column(c).Grow(1).FillHeight().Background(t.Background).Children(func() {
			a.topBar(c)
			ui.Column(c).Grow(1).Padding(16).Children(func() {
				switch a.page {
				case PageDashboard:
					a.dashboardPage(c)
				case PageDownloads:
					a.downloadsPage(c)
				case PageConverter:
					a.converterPage(c)
				case PageHistory:
					a.historyPage(c)
				case PageSettings:
					a.settingsPage(c)
				case PageAbout:
					a.aboutPage(c)
				}
			})
			a.toastBar(c)
		})
	})
}

func (a *App) topBar(c *ui.Context) {
	t := c.Theme()
	titles := map[string]string{
		PageDashboard: "Dashboard", PageDownloads: "Downloads", PageConverter: "Converter",
		PageHistory: "History", PageSettings: "Settings", PageAbout: "About",
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
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c, &a.urlInput).Placeholder("Paste video or playlist URL…").Grow(1)
			btn := ui.PrimaryButton(c, "Analyze")
			if a.analyzing {
				btn.Disabled(true)
			}
			if btn.Clicked() && !a.analyzing {
				go a.doAnalyze()
			}
			if a.analyzing {
				ui.Spinner(c)
			}
		})
		if a.analyzeError != "" {
			ui.Text(c, a.analyzeError).FontSize(13).TextColor(t.Danger)
		}
		if a.currentMedia != nil {
			a.mediaCard(c)
			a.configPanel(c)  // resolution, container, mode — always visible
			a.formatPicker(c) // optional advanced format IDs (collapsed)
		} else if a.currentPlaylist != nil {
			a.playlistPanel(c)
		} else if !a.analyzing {
			ui.Column(c).FillWidth().Padding(40, 0).Center().Gap(6).Children(func() {
				ui.Text(c, "Paste a link to get started").FontSize(17).TextColor(t.TextMuted)
				ui.Text(c, "YouTube · TikTok · Instagram · X · ARY Plus · 1000+ sites").FontSize(12).TextColor(t.TextMuted)
			})
		}
	})
}

func (a *App) mediaCard(c *ui.Context) {
	t := c.Theme()
	m := a.currentMedia
	ui.Column(c).FillWidth().Background(t.Surface).Padding(14).Radius(10).Gap(4).Children(func() {
		ui.Text(c, m.Title).FontSize(15).Bold()
		meta := m.Uploader
		if m.Duration > 0 {
			meta += " · " + formatDuration(m.Duration)
		}
		if m.Extractor != "" {
			meta += " · " + m.Extractor
		}
		ui.Text(c, meta).FontSize(12).TextColor(t.TextMuted)
		if len(m.AudioTracks) > 1 {
			ui.Text(c, fmt.Sprintf("%d audio tracks", len(m.AudioTracks))).FontSize(12).TextColor(t.Accent)
		}
		if len(m.Subtitles) > 0 {
			ui.Text(c, fmt.Sprintf("%d subtitle languages", len(m.Subtitles))).FontSize(12).TextColor(t.Accent)
		}
	})
}

func (a *App) formatPicker(c *ui.Context) {
	t := c.Theme()
	m := a.currentMedia
	if m == nil || len(m.Formats) == 0 {
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

func (a *App) configPanel(c *ui.Context) {
	t := c.Theme()
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
				if ui.Select(c, &ct, []string{"mp4", "mkv", "webm"}).Changed() {
					a.container = ct
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
		if a.currentMedia != nil && len(a.currentMedia.AudioTracks) > 1 {
			ui.Checkbox(c, &a.multiAudio, "Keep multiple audio tracks (mkv)")
			if a.multiAudio {
				ui.Column(c).Gap(2).Padding(0, 0, 0, 20).Children(func() {
					for _, tr := range a.currentMedia.AudioTracks {
						id := tr.ID
						on := a.selectedAudio[id]
						label := tr.Language
						if label == "" {
							label = id
						}
						if tr.Note != "" {
							label += " · " + tr.Note
						}
						ui.Checkbox(c.Key("aud-"+id), &on, label)
						a.selectedAudio[id] = on
					}
				})
			}
		}
		ui.Checkbox(c, &a.writeSubs, "Download & embed subtitles")
		if ui.PrimaryButton(c, "Start download").Clicked() {
			a.startDownload()
		}
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
			on := a.selectedEntries[i]
			ui.Row(c).FillWidth().Padding(4, 6).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Checkbox(c.Key(fmt.Sprintf("pe-%d", i)), &on, "")
				a.selectedEntries[i] = on
				ui.Column(c).Grow(1).Children(func() {
					ui.Text(c, ent.Title).FontSize(13)
					if ent.Duration > 0 {
						ui.Text(c, formatDuration(ent.Duration)).FontSize(11).TextColor(t.TextMuted)
					}
				})
			})
		}).Grow(1)
		if ui.PrimaryButton(c, "Download selected").Clicked() {
			a.startPlaylistDownloads()
		}
	})
}

func (a *App) doAnalyze() {
	a.analyzing = true
	a.analyzeError = ""
	a.currentMedia = nil
	a.currentPlaylist = nil
	a.selectedEntries = map[int]bool{}
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
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result, err := a.ytdlp.Analyze(ctx, a.urlInput)
	if err != nil {
		a.analyzing = false
		a.analyzeError = err.Error()
		a.invalidate()
		return
	}
	switch v := result.(type) {
	case *models.MediaInfo:
		full, err := a.ytdlp.FetchFormats(ctx, a.urlInput)
		if err == nil {
			a.currentMedia = full
		} else {
			a.currentMedia = v
		}
		a.rebuildFormatOptions()
	case *models.PlaylistInfo:
		a.currentPlaylist = v
	}
	a.analyzing = false
	a.invalidate()
}

func (a *App) startDownload() {
	if a.currentMedia == nil {
		return
	}
	req := a.buildRequest(a.currentMedia.WebpageURL, a.currentMedia.Title, a.currentMedia.Thumbnail, "", 0)
	a.manager.Enqueue(req)
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
		a.manager.Enqueue(req)
		n++
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

func (a *App) buildRequest(url, title, thumb, playlistTitle string, index int) models.DownloadRequest {
	req := models.DownloadRequest{
		URL: url, Title: title, Mode: a.mode, OutputDir: a.settings.DownloadDir,
		Container: a.container, MaxHeight: a.maxHeight, AudioFormat: a.audioFormat,
		AudioBitrateKbps: 0, // 0 = best available (yt-dlp ba* / opus preferred)
		WriteSubtitles:   a.writeSubs, MultiAudio: a.multiAudio,
		EmbedThumbnail: a.settings.EmbedThumbnail, EmbedMetadata: a.settings.EmbedMetadata,
		PreferVP9: a.settings.PreferVP9Video, ThumbnailURL: thumb,
		PlaylistTitle: playlistTitle, PlaylistIndex: index,
		VideoFormatID: a.selectedVideoFormat, AudioFormatID: a.selectedAudioFormat,
	}
	if a.multiAudio {
		for id, on := range a.selectedAudio {
			if on {
				req.SelectedAudioTrackIDs = append(req.SelectedAudioTrackIDs, id)
			}
		}
	}
	return req
}

func (a *App) downloadsPage(c *ui.Context) {
	t := c.Theme()
	a.tasksMu.Lock()
	tasks := append([]*models.DownloadTask(nil), a.tasks...)
	a.tasksMu.Unlock()
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.After(tasks[j].CreatedAt) })
	if len(tasks) == 0 {
		ui.Column(c).Grow(1).Center().Children(func() {
			ui.Text(c, "No downloads yet").FontSize(15).TextColor(t.TextMuted)
		})
		return
	}
	ui.List(c, &a.dlListState, len(tasks), func(i int) {
		task := tasks[i]
		ui.Column(c).FillWidth().Background(t.Surface).Padding(12).Radius(8).Gap(4).Children(func() {
			ui.Row(c).FillWidth().Children(func() {
				ui.Text(c, task.Request.Title).FontSize(13).Bold().Grow(1)
				ui.Text(c, string(task.State)).FontSize(11).TextColor(stateColor(c, task.State))
			})
			if task.State == models.StateDownloading || task.State == models.StateProcessing {
				pct := task.Progress.Percent / 100
				if pct <= 0 {
					pct = -1
				}
				ui.Progress(c, pct)
				ui.Row(c).FillWidth().Children(func() {
					ui.Text(c, fmt.Sprintf("%.1f%%", task.Progress.Percent)).FontSize(11).TextColor(t.TextMuted)
					ui.Spacer(c).Grow(1)
					if task.Progress.SpeedBps > 0 {
						ui.Text(c, formatSpeed(task.Progress.SpeedBps)).FontSize(11).TextColor(t.TextMuted)
					}
				})
			}
			if task.State == models.StateError && task.Error != "" {
				ui.Text(c, task.Error).FontSize(11).TextColor(t.Danger)
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
					ui.Text(c, filepath.Base(task.OutputPath)).FontSize(11).TextColor(t.TextMuted)
					path := task.OutputPath
					if ui.Button(c.Key("show-"+task.ID), "Show in folder").Clicked() {
						go mygo.Shell.ShowItemInFolder(path)
					}
				}
			})
		})
	}).Grow(1).Gap(8)
}

func stateColor(c *ui.Context, s models.DownloadState) ui.Color {
	t := c.Theme()
	switch s {
	case models.StateCompleted:
		return t.Success
	case models.StateError:
		return t.Danger
	case models.StateDownloading, models.StateProcessing:
		return t.Accent
	default:
		return t.TextMuted
	}
}

func (a *App) converterPage(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).Gap(12).Children(func() {
		ui.Text(c, "Media Converter").FontSize(15).Bold()
		if !a.ffmpeg.Available() {
			ui.Text(c, "FFmpeg not found on PATH.").FontSize(13).TextColor(t.Danger)
			return
		}
		ui.Column(c).FillWidth().Background(t.Surface).Padding(14).Radius(10).Gap(10).Children(func() {
			// Drop zone + path + Browse
			drop := ui.Column(c).FillWidth().Padding(16).Radius(8).
				Border(1, t.Border).AlignItems(ui.Center).Gap(6)
			if drop.FileDragOver() {
				drop.Border(2, t.Accent)
			}
			if files := drop.DroppedFiles(); files != nil && len(files) > 0 {
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
				})
				a.toggleBtn(c, "ck-v", "Video", a.convKind == models.KindVideo, func() {
					a.convKind = models.KindVideo
					a.convFormat = "mp4"
				})
			})
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Target").FontSize(12).TextColor(t.TextMuted).Width(90)
				formats := []string{"mp3", "m4a", "opus", "flac", "wav"}
				if a.convKind == models.KindVideo {
					formats = []string{"mp4", "mkv", "webm"}
				}
				for _, f := range formats {
					f := f
					a.toggleBtn(c, "cf-"+f, f, a.convFormat == f, func() {
						a.convFormat = f
					})
				}
			})
			if ui.PrimaryButton(c, "Convert").Clicked() {
				src := strings.TrimSpace(a.convSource)
				if src == "" {
					a.showToast("Enter a source path")
					return
				}
				a.conv.Enqueue(models.ConversionRequest{
					Source: src, TargetFormat: a.convFormat, Kind: a.convKind,
					Mode: models.ConversionReencode, KeepOriginal: a.settings.KeepOriginals,
				})
			}
		})
		a.convJobsMu.Lock()
		jobs := append([]*models.ConversionJob(nil), a.convJobs...)
		a.convJobsMu.Unlock()
		sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.After(jobs[j].CreatedAt) })
		if len(jobs) > 0 {
			ui.Text(c, "Jobs").FontSize(13).Bold()
			ui.List(c, &a.convListState, len(jobs), func(i int) {
				j := jobs[i]
				ui.Column(c).FillWidth().Background(t.Surface).Padding(10).Radius(8).Gap(4).Children(func() {
					ui.Row(c).FillWidth().Children(func() {
						ui.Text(c, filepath.Base(j.Request.Source)).FontSize(13).Bold().Grow(1)
						ui.Text(c, string(j.State)).FontSize(11).TextColor(t.TextMuted)
					})
					if j.State == models.ConvRunning {
						ui.Progress(c, j.Percent/100)
					}
					if j.State == models.ConvError {
						ui.Text(c, j.Error).FontSize(11).TextColor(t.Danger)
					}
					if j.State == models.ConvCompleted {
						ui.Text(c, j.OutputPath).FontSize(11).TextColor(t.TextMuted)
					}
					if !j.State.IsTerminal() {
						if ui.Button(c.Key("cc-"+j.ID), "Cancel").Clicked() {
							a.conv.Cancel(j.ID)
						}
					}
				})
			}).Grow(1).Gap(6)
		}
	})
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
	ui.Column(c).Grow(1).Gap(10).Children(func() {
		ui.Row(c).Children(func() {
			ui.Text(c, "History").FontSize(15).Bold()
			ui.Spacer(c).Grow(1)
			if ui.Button(c, "Refresh").Clicked() {
				a.historyLoaded = false
			}
			if ui.Button(c, "Clear").Clicked() {
				_ = a.history.Clear()
				a.historyLoaded = false
			}
		})
		if len(a.historyEntries) == 0 {
			ui.Column(c).Grow(1).Center().Children(func() {
				ui.Text(c, "No history yet").FontSize(14).TextColor(t.TextMuted)
			})
			return
		}
		ui.List(c, &a.histListState, len(a.historyEntries), func(i int) {
			e := a.historyEntries[i]
			ui.Column(c).FillWidth().Background(t.Surface).Padding(10).Radius(8).Gap(2).Children(func() {
				ui.Row(c).FillWidth().Children(func() {
					ui.Text(c, e.Title).FontSize(13).Bold().Grow(1)
					ui.Text(c, e.State).FontSize(11).TextColor(t.TextMuted)
				})
				ui.Text(c, e.URL).FontSize(11).TextColor(t.TextMuted)
				if e.OutputPath != "" {
					ui.Text(c, e.OutputPath).FontSize(11).TextColor(t.TextMuted)
				}
				if e.Error != "" {
					ui.Text(c, e.Error).FontSize(11).TextColor(t.Danger)
				}
			})
		}).Grow(1).Gap(6)
	})
}

func (a *App) settingsPage(c *ui.Context) {
	t := c.Theme()
	ui.Scroll(c).Grow(1).Gap(14).Children(func() {
		ui.Text(c, "Settings").FontSize(15).Bold()
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Download folder").FontSize(13).Width(130)
			ui.TextInput(c, &a.settings.DownloadDir).Grow(1)
			if ui.Button(c, "Browse…").Clicked() {
				go a.pickDownloadDir()
			}
		})
		maxC := float64(a.settings.MaxConcurrent)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Max concurrent").FontSize(13).Width(130)
			if ui.Slider(c, &maxC, 1, 16).Changed() {
				a.settings.MaxConcurrent = int(maxC)
				a.manager.SetMaxWorkers(a.settings.MaxConcurrent)
			}
			ui.Text(c, fmt.Sprintf("%d", a.settings.MaxConcurrent)).FontSize(13).TextColor(t.TextMuted).Width(24)
		})
		ui.Checkbox(c, &a.settings.EmbedThumbnail, "Embed thumbnail")
		ui.Checkbox(c, &a.settings.EmbedMetadata, "Embed metadata & chapters")
		ui.Checkbox(c, &a.settings.PreferVP9Video, "Prefer VP9 / AV1 when available")
		ui.Checkbox(c, &a.settings.KeepOriginals, "Keep originals after conversion")
		ui.Checkbox(c, &a.settings.WriteSubtitles, "Write subtitles by default")
		ui.Checkbox(c, &a.settings.MultiAudio, "Multi-audio by default")
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Proxy").FontSize(13).Width(130)
			ui.TextInput(c, &a.settings.Proxy).Placeholder("http://… or socks5://…").Grow(1)
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Cookies browser").FontSize(13).Width(130)
			ui.TextInput(c, &a.settings.CookiesBrowser).Placeholder("chrome, firefox, edge…").Grow(1)
		})
		if ui.PrimaryButton(c, "Save settings").Clicked() {
			_ = a.settingsRepo.Save(a.settings)
			a.showToast("Settings saved")
		}
		ff := a.ffmpeg.Path()
		if ff == "" {
			ff = "not found"
		}
		yt := "not found"
		if a.ytdlp != nil {
			if b := a.ytdlp.Bin(); b != "" {
				yt = b
			}
		}
		ui.Text(c, "FFmpeg: "+ff).FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, "yt-dlp: "+yt).FontSize(12).TextColor(t.TextMuted)
	})
}

func (a *App) aboutPage(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
		ui.Text(c, "Video Downloader").FontSize(22).Bold()
		ui.Text(c, "v2.0.0 · MyGo native UI").FontSize(13).TextColor(t.TextMuted)
		ui.Text(c, "Rewritten from the Flet + Python original by Fazi Gondal").FontSize(13).TextColor(t.TextMuted)
		ui.Text(c, "yt-dlp · FFmpeg · MyGo").FontSize(12).TextColor(t.TextMuted)
	})
}

func (a *App) pickConvertFile() {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: a.win,
		Title:  "Choose media file",
		Filters: []mygo.FileFilter{
			{Name: "Media", Extensions: []string{"mp4", "mkv", "webm", "avi", "mov", "m4a", "mp3", "flac", "wav", "opus", "aac"}},
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
	a.convSource = paths[0]
	a.invalidate()
}

func (a *App) pickDownloadDir() {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:    a.win,
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
	a.settings.DownloadDir = paths[0]
	a.invalidate()
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
