package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"github.com/fazi-gondal/video-downloader-mygo/internal/i18n"
	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
	"github.com/fazi-gondal/video-downloader-mygo/internal/services"
	appui "github.com/fazi-gondal/video-downloader-mygo/internal/ui"
)

// tr translates key for the app's current language.
func (a *App) tr(key string, args ...map[string]any) string {
	var params map[string]any
	if len(args) > 0 {
		params = args[0]
	}
	return i18n.T(a.lang, key, params)
}

// SetInitialLocale picks the UI language before the first frame, so the app
// never paints the source language and then switches.
func (a *App) SetInitialLocale() {
	a.lang = a.preferredLocale()
	mygo.App.SetLocale(a.lang)
}

// preferredLocale resolves the language setting: an explicit choice wins,
// otherwise the user's preferred languages decide.
func (a *App) preferredLocale() string {
	switch a.settingsDraft.Language {
	case i18n.English:
		return i18n.English
	case i18n.Chinese:
		return i18n.Chinese
	}
	for _, tag := range mygo.App.PreferredLanguages() {
		if i18n.Detect(tag) == i18n.Chinese {
			return i18n.Chinese
		}
	}
	return i18n.English
}

// extractItem is one player found on a page, with the metadata yt-dlp could
// recover for it. Probing runs in the background so a page with many players
// shows up immediately.
type extractItem struct {
	video   services.SiteVideo
	media   *models.MediaInfo
	probing bool
}

func (i extractItem) duration() float64 {
	if i.media == nil {
		return 0
	}
	return i.media.Duration
}

func (i extractItem) height() int {
	return mediaHeight(i.media)
}

func mediaHeight(media *models.MediaInfo) int {
	if media == nil {
		return 0
	}
	best := 0
	for _, format := range media.Formats {
		if height := formatHeight(format); height > best {
			best = height
		}
	}
	return best
}

// extractorCollapsible is the page extractor, folded into the Home hero. The
// trigger keeps the same spot under the search field and the panel grows from
// it, so the page never swaps to a different layout.
func (a *App) extractorCollapsible(c *ui.Context, panelWidth float32) {
	t := c.Theme()
	parts := ui.CollapsibleBase(c, &a.extractOpen)
	progress := parts.Progress()
	a.extractProgress = progress
	parts.Trigger.
		AlignSelf(ui.Center).
		Background(t.Surface).
		Border(1, t.Border).
		Radius(999).
		Padding(8, 16).
		Gap(6).
		Children(func() {
			ui.Icon(c, appui.IconWand).FontSize(14).TextColor(t.Accent)
			label := a.tr("extractor.link")
			if a.extractOpen {
				label = a.tr("extractor.collapse")
			}
			ui.Text(c, label).FontSize(13)
		})
	parts.Panel(func() {
		ui.Column(c).Width(panelWidth).MaxWidth(720).AlignSelf(ui.Center).Gap(8).Padding(12, 0, 0, 0).Opacity(progress).Children(func() {
			// Input card — same shape as the Home search field.
			ui.Row(c).FillWidth().Material(glass.Glass{Clarity: 0.25}).Border(1, t.Border).Radius(12).Padding(8, 10).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, appui.IconWand).FontSize(18).TextColor(t.TextMuted).Width(20)
				ui.TextInput(c, &a.extractInput).Label(a.tr("extractor.urlLabel")).Placeholder(a.tr("extractor.placeholder")).Grow(1).OnSubmit(a.startExtract)
				btn := ui.PrimaryButton(c.Key("extract-resolve"), a.tr("extractor.resolve"))
				if a.extractBusy {
					btn.Disabled(true)
				}
				if btn.Clicked() {
					a.startExtract()
				}
			})

			ui.Text(c, a.tr("extractor.hint")).FontSize(11).TextColor(t.TextMuted)

			if a.extractError != "" {
				ui.Row(c).FillWidth().Background(t.Surface).Border(1, t.Danger).Radius(10).Padding(10, 12).Children(func() {
					ui.Text(c, a.extractError).FontSize(13).TextColor(t.Danger).Grow(1)
				})
			}

			if a.extractBusy {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c)
					ui.Text(c, a.tr("extractor.busy")).FontSize(12).TextColor(t.TextMuted)
				})
			}

			if a.extractResult != nil {
				a.extractorResultCard(c)
			}
		})
	})
}

func (a *App) extractorResultCard(c *ui.Context) {
	t := c.Theme()
	res := a.extractResult
	title := strings.TrimSpace(res.Title)
	if title == "" {
		title = a.tr("extractor.fallback")
	}

	selected := 0
	for i := range a.extractItems {
		if a.extractSelected[i] {
			selected++
		}
	}

	ui.Column(c).FillWidth().MaxWidth(720).Material(glass.Glass{Clarity: 0.2}).Border(1, t.Border).Radius(14).Padding(14, 16).Gap(10).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).Children(func() {
			ui.Box(c).Size(32, 32).Background(t.Accent).Radius(9).Center().Children(func() {
				ui.Icon(c, appui.IconWand).FontSize(16).TextColor(ui.Color{R: 255, G: 255, B: 255, A: 255})
			})
			ui.Column(c).Grow(1).Gap(2).Children(func() {
				ui.Text(c, title).FontSize(15).Bold().MaxWidth(540).Ellipsis("…")
				meta := i18n.Count(a.lang, len(res.Videos), "extractor.countOne", "extractor.countMany")
				if host := hostLabel(res.PageURL); host != "" {
					meta += " · " + host
				}
				ui.Text(c, meta).FontSize(11).TextColor(t.TextMuted).Ellipsis("…")
			})
		})

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c.Key("extract-select-all"), a.tr("extractor.selectAll")).Clicked() {
				for i := range a.extractItems {
					a.extractSelected[i] = true
				}
				a.invalidate()
			}
			if ui.Button(c.Key("extract-select-none"), a.tr("extractor.selectNone")).Clicked() {
				a.extractSelected = map[int]bool{}
				a.invalidate()
			}
			ui.Spacer(c).Grow(1)
			ui.Text(c, a.tr("extractor.selected", map[string]any{"n": selected})).FontSize(12).TextColor(t.TextMuted)
		})

		ui.Box(c).FillWidth().Height(1).Background(t.Border)

		for i := range a.extractItems {
			index := i
			item := a.extractItems[i]
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
				on := a.extractSelected[index]
				checkbox := ui.Checkbox(c.Key(fmt.Sprintf("extract-entry-%d", index)), &on, "").Label(fmt.Sprintf("extract-entry-label-%d", index))
				if checkbox.Changed() {
					a.extractSelected[index] = on
					a.invalidate()
				}
				ui.Column(c).Grow(1).Gap(1).Children(func() {
					ui.Text(c, a.extractItemTitle(index)).FontSize(13).MaxWidth(500).Ellipsis("…")
					ui.Text(c, a.extractItemMeta(item)).FontSize(11).TextColor(t.TextMuted).Ellipsis("…")
				})
				if ui.Button(c.Key(fmt.Sprintf("extract-copy-%d", index)), a.tr("extractor.copy")).Clicked() {
					mygo.Clipboard.WriteText(item.video.MediaURL)
					a.showToast(a.tr("extractor.copiedOne"))
				}
			})
		}

		ui.Box(c).FillWidth().Height(1).Background(t.Border)

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c.Key("extract-copy-all"), a.tr("extractor.copyAll")).Clicked() {
				urls := make([]string, 0, len(res.Videos))
				for _, video := range res.Videos {
					urls = append(urls, video.MediaURL)
				}
				mygo.Clipboard.WriteText(strings.Join(urls, "\n"))
				a.showToast(a.tr("extractor.copiedMany", map[string]any{"n": len(urls)}))
			}
			if ui.Button(c.Key("extract-download-all"), a.tr("extractor.downloadAll")).Clicked() {
				a.downloadAllExtracted()
			}
			ui.Spacer(c).Grow(1)
			label := a.tr("extractor.continueOne")
			if selected > 1 {
				label = a.tr("extractor.continue", map[string]any{"n": selected})
			}
			if ui.PrimaryButton(c.Key("extract-continue"), label).Clicked() {
				a.continueExtract()
			}
		})
	})
}

func (a *App) extractItemTitle(index int) string {
	if title := strings.TrimSpace(a.extractItems[index].video.Title); title != "" {
		return title
	}
	return a.tr("extractor.videoN", map[string]any{"n": index + 1})
}

func (a *App) extractItemMeta(item extractItem) string {
	parts := make([]string, 0, 4)
	if item.probing {
		parts = append(parts, a.tr("extractor.probing"))
	}
	if duration := item.duration(); duration > 0 {
		parts = append(parts, formatDuration(duration))
	}
	if height := item.height(); height > 0 {
		parts = append(parts, fmt.Sprintf("%dp", height))
	}
	parts = append(parts, strings.ToUpper(item.video.Format))
	return strings.Join(parts, " · ")
}

func hostLabel(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func (a *App) startExtract() {
	if a.extractBusy {
		return
	}
	raw := strings.TrimSpace(a.extractInput)
	if raw == "" {
		a.extractError = a.tr("extractor.emptyInput")
		a.invalidate()
		return
	}
	if a.extractCancel != nil {
		a.extractCancel()
	}
	probeCtx, cancel := context.WithCancel(context.Background())
	a.extractCancel = cancel
	a.extractToken++
	token := a.extractToken
	a.extractBusy = true
	a.extractError = ""
	a.extractResult = nil
	a.extractItems = nil
	a.extractSelected = map[int]bool{}
	a.invalidate()
	go a.doExtract(probeCtx, token, raw)
}

func (a *App) doExtract(ctx context.Context, token uint64, pageURL string) {
	fetchCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	res, err := services.ExtractSiteMedia(fetchCtx, pageURL)
	cancel()
	if err != nil {
		a.updateOnMain(func() {
			if token != a.extractToken {
				return
			}
			a.extractBusy = false
			a.extractError = a.extractErrorText(err)
			a.invalidate()
		})
		return
	}

	a.updateOnMain(func() {
		if token != a.extractToken {
			return
		}
		a.extractBusy = false
		a.extractResult = res
		a.extractItems = make([]extractItem, len(res.Videos))
		a.extractSelected = make(map[int]bool, len(res.Videos))
		for i, video := range res.Videos {
			a.extractItems[i] = extractItem{video: video, probing: true}
			a.extractSelected[i] = true
		}
		a.invalidate()
	})
	a.probeExtractVideos(ctx, token, res.Videos)
}

// probeExtractVideos fills in duration and best height for each player with a
// small worker pool, so many-player pages finish quickly. A failed probe only
// leaves the row without metadata; the stream is still usable. ctx is
// cancelled when the user resolves another page.
func (a *App) probeExtractVideos(ctx context.Context, token uint64, videos []services.SiteVideo) {
	const workers = 3
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range videos {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		index := i
		video := videos[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			media, err := a.probeVideo(probeCtx, video)
			cancel()

			a.updateOnMain(func() {
				if token != a.extractToken || index >= len(a.extractItems) {
					return
				}
				item := a.extractItems[index]
				item.probing = false
				if err == nil {
					item.media = media
				}
				a.extractItems[index] = item
				a.invalidate()
			})
		}()
	}
	wg.Wait()
}

func (a *App) probeVideo(ctx context.Context, video services.SiteVideo) (*models.MediaInfo, error) {
	analyzed, err := a.ytdlp.Analyze(ctx, video.MediaURL)
	if err != nil {
		return nil, err
	}
	media, ok := analyzed.(*models.MediaInfo)
	if !ok || media == nil {
		return nil, errors.New("resolved URL is a playlist")
	}
	return media, nil
}

func (a *App) extractErrorText(err error) string {
	switch {
	case errors.Is(err, services.ErrEncryptedConfig):
		return a.tr("extractor.errEncrypted")
	case errors.Is(err, services.ErrNoPlayableMedia):
		return a.tr("extractor.errNoMedia")
	case errors.Is(err, services.ErrNonPublicPage):
		return a.tr("extractor.errPrivate")
	default:
		return a.tr("extractor.errLoad", map[string]any{"err": err})
	}
}

// continueExtract sends the selected players to the download-options page.
func (a *App) continueExtract() {
	res := a.extractResult
	if res == nil {
		return
	}
	chosen := make([]int, 0, len(a.extractItems))
	for i := range a.extractItems {
		if a.extractSelected[i] {
			chosen = append(chosen, i)
		}
	}
	if len(chosen) == 0 {
		a.showToast(a.tr("extractor.nothing"))
		a.invalidate()
		return
	}

	if len(chosen) == 1 {
		item := a.extractItems[chosen[0]]
		if item.media != nil {
			media := *item.media
			media.Title = a.extractItemTitle(chosen[0])
			if media.Thumbnail == "" {
				media.Thumbnail = res.Thumbnail
			}
			media.OriginalURL = res.PageURL
			media.SourcePageURL = res.PageURL
			media.SourceIndex = chosen[0]
			a.resetMediaSelection()
			a.currentMedia = &media
			a.page = PageConfig
			a.invalidate()
			return
		}
	}

	entries := make([]models.PlaylistEntry, 0, len(chosen))
	for n, index := range chosen {
		item := a.extractItems[index]
		entries = append(entries, models.PlaylistEntry{
			ID:            item.video.ID,
			Title:         a.extractItemTitle(index),
			URL:           item.video.MediaURL,
			Duration:      item.duration(),
			Thumbnail:     res.Thumbnail,
			Index:         n,
			SourcePageURL: res.PageURL,
			SourceIndex:   index,
		})
	}
	playlistTitle := strings.TrimSpace(res.Title)
	if playlistTitle == "" {
		playlistTitle = a.tr("extractor.fallback")
	}

	a.resetMediaSelection()
	a.currentPlaylist = &models.PlaylistInfo{
		Title:       playlistTitle,
		Uploader:    hostLabel(res.PageURL),
		WebpageURL:  res.PageURL,
		OriginalURL: res.PageURL,
		Entries:     entries,
	}
	for i := range entries {
		a.selectedEntries[i] = true
	}
	a.page = PageConfig
	a.invalidate()
}

// downloadAllExtracted queues every recovered video with the current defaults,
// skipping the options page for people who just want the files.
func (a *App) downloadAllExtracted() {
	res := a.extractResult
	if res == nil || len(a.extractItems) == 0 {
		return
	}
	if !a.requireTools(false) {
		return
	}
	playlistTitle := strings.TrimSpace(res.Title)
	if playlistTitle == "" {
		playlistTitle = a.tr("extractor.fallback")
	}
	requests := make([]models.DownloadRequest, 0, len(a.extractItems))
	for i := range a.extractItems {
		item := a.extractItems[i]
		req := a.buildBestQualityRequest(item.video.MediaURL, a.extractItemTitle(i), res.Thumbnail, playlistTitle, i)
		req.SourcePageURL = res.PageURL
		req.SourceIndex = i
		requests = append(requests, req)
	}
	a.page = PageDownloads
	a.showToast(a.tr("downloads.queueingMany", map[string]any{"n": len(requests)}))
	go func() {
		for _, req := range requests {
			if _, err := a.manager.EnqueueContext(context.Background(), req); err != nil {
				err := err
				a.updateOnMain(func() { a.showToast(a.tr("Could not queue download: ") + err.Error()) })
			}
		}
	}()
}

// heroContentWidth is the hero's content width: the window minus the sidebar,
// the page padding (16 each side) and the hero padding (40 each side), capped
// at 720. Panels inside a shrink-to-fit container need it explicitly.
func heroContentWidth(c *ui.Context) float32 {
	windowWidth, _ := c.Size()
	width := windowWidth - sidebarWidth - 2*pageInset - 2*heroInset
	if width > contentMaxWidth {
		width = contentMaxWidth
	}
	if width < contentMinWidth {
		width = contentMinWidth
	}
	return width
}

// pageContentWidth is the width available inside a normal page: window minus
// sidebar and page padding, capped at 720.
func pageContentWidth(c *ui.Context) float32 {
	windowWidth, _ := c.Size()
	width := windowWidth - sidebarWidth - 2*pageInset
	if width > contentMaxWidth {
		width = contentMaxWidth
	}
	if width < contentMinWidth {
		width = contentMinWidth
	}
	return width
}

// openExtractorFor points Home at a page whose direct link is no longer usable.
func (a *App) openExtractorFor(pageURL string) {
	a.extractInput = pageURL
	a.extractOpen = true
	a.page = PageDashboard
	a.showToast(a.tr("history.requeueExpired"))
	a.invalidate()
}

// buildBestQualityRequest builds a request that ignores whatever the download
// options page was last set to: "Download all (best quality)" must not inherit
// a stale audio-only mode, height cap, or explicit format id.
func (a *App) buildBestQualityRequest(url, title, thumb, playlistTitle string, index int) models.DownloadRequest {
	settings := a.settingsDraftSnapshot()
	return models.DownloadRequest{
		URL: url, Title: title, Mode: models.ModeVideoAudio,
		OutputDir:             settings.DownloadDir,
		Container:             "mp4",
		EmbedThumbnail:        settings.EmbedThumbnail,
		EmbedMetadata:         settings.EmbedMetadata,
		PreferVP9:             settings.PreferVP9Video,
		PlaylistTitle:         playlistTitle,
		PlaylistIndex:         index,
		ThumbnailURL:          thumb,
		AudioSelectionMode:    models.AudioSelectionDefault,
		AudioSelection:        models.AudioSelectionDefault,
		SubtitleSelectionMode: models.SubtitleSelectionDefault,
		SubtitleSelection:     models.SubtitleSelectionDefault,
	}
}
