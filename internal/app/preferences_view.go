package app

import (
	"fmt"
	"net/url"
	"runtime"
	"strconv"
	"strings"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"github.com/fazi-gondal/video-downloader-mygo/internal/config"
	"github.com/fazi-gondal/video-downloader-mygo/internal/tools"
	appui "github.com/fazi-gondal/video-downloader-mygo/internal/ui"
)

// refinedSettingsDraft contains the values that are represented as text in
// the settings form. Keeping the conversion here makes save validation
// independent from the UI and prevents partially parsed settings from being
// persisted.
type refinedSettingsDraft struct {
	settings       config.AppSettings
	maxConcurrent  string
	concurrentFrag string
	rateLimit      string
	headers        string
	subtitleLangs  string
}

func parseSettingsInt(raw, field string, min, max int) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number", field)
	}
	if value < min || value > max {
		return 0, fmt.Errorf("%s must be between %d and %d", field, min, max)
	}
	return value, nil
}

func parseSettingsHeaders(raw string) (map[string]string, error) {
	headers := make(map[string]string)
	seenNames := make(map[string]bool)
	for lineNumber, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("custom header line %d must use Key: Value", lineNumber+1)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			return nil, fmt.Errorf("custom header line %d has an empty name", lineNumber+1)
		}
		if value == "" {
			return nil, fmt.Errorf("custom header %q has an empty value", key)
		}
		canonicalKey := strings.ToLower(key)
		if seenNames[canonicalKey] {
			return nil, fmt.Errorf("custom header %q is duplicated", key)
		}
		headers[key] = value
		seenNames[canonicalKey] = true
	}
	return headers, nil
}

func parseSubtitleLanguages(raw string) []string {
	seen := make(map[string]bool)
	langs := make([]string, 0)
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ';'
	}) {
		lang := strings.TrimSpace(item)
		if lang == "" || seen[lang] {
			continue
		}
		seen[lang] = true
		langs = append(langs, lang)
	}
	return langs
}

func validateSettingsDraft(draft refinedSettingsDraft) (config.AppSettings, error) {
	settings := draft.settings
	var err error
	settings.MaxConcurrent, err = parseSettingsInt(draft.maxConcurrent, "maximum concurrent downloads", 1, 16)
	if err != nil {
		return config.AppSettings{}, err
	}
	settings.ConcurrentFragments, err = parseSettingsInt(draft.concurrentFrag, "concurrent fragments", 1, 32)
	if err != nil {
		return config.AppSettings{}, err
	}
	settings.RateLimitKbps, err = parseSettingsInt(draft.rateLimit, "rate limit", 0, 1_000_000)
	if err != nil {
		return config.AppSettings{}, err
	}
	settings.CustomHeaders, err = parseSettingsHeaders(draft.headers)
	if err != nil {
		return config.AppSettings{}, err
	}
	if strings.TrimSpace(settings.DownloadDir) == "" {
		return config.AppSettings{}, fmt.Errorf("download folder cannot be empty")
	}
	if settings.Proxy != "" {
		proxy, parseErr := url.Parse(strings.TrimSpace(settings.Proxy))
		proxyScheme := ""
		if proxy != nil {
			proxyScheme = strings.ToLower(proxy.Scheme)
		}
		validProxyScheme := proxyScheme == "http" || proxyScheme == "https" ||
			proxyScheme == "socks4" || proxyScheme == "socks4a" ||
			proxyScheme == "socks5" || proxyScheme == "socks5h"
		if parseErr != nil || proxy == nil || proxy.Host == "" || !validProxyScheme {
			return config.AppSettings{}, fmt.Errorf("proxy must be a valid URL")
		}
		settings.Proxy = proxy.String()
	}
	settings.CookiesBrowser = strings.TrimSpace(settings.CookiesBrowser)
	if strings.ContainsAny(settings.CookiesBrowser, " \t\r\n") || strings.HasPrefix(settings.CookiesBrowser, "-") {
		return config.AppSettings{}, fmt.Errorf("cookies browser must be a browser/profile value without spaces")
	}
	settings.SubtitleLangs = parseSubtitleLanguages(draft.subtitleLangs)
	if settings.WriteSubtitles && len(settings.SubtitleLangs) == 0 {
		return config.AppSettings{}, fmt.Errorf("choose at least one subtitle language or use all")
	}
	if settings.ThemeMode != "system" && settings.ThemeMode != "dark" && settings.ThemeMode != "light" {
		return config.AppSettings{}, fmt.Errorf("theme must be system, dark, or light")
	}
	return settings, nil
}

func (a *App) refinedSettingsPage(c *ui.Context) {
	a.ensureSettingsDraft()
	t := c.Theme()
	page := ui.Scroll(c).Grow(1).Gap(14)

	page.Children(func() {
		settingsCard(c, appui.IconSettings, a.tr("Appearance"), a.tr("Choose how MyGo follows your system theme."), func() {
			ui.Row(c).FillWidth().Gap(10).Children(func() {
				a.themeCard(c, "light", appui.IconSun, a.tr("Light"))
				a.themeCard(c, "dark", appui.IconMoon, a.tr("Dark"))
				a.themeCard(c, "system", appui.IconMonitor, a.tr("System"))
			})
		})

		settingsCard(c, appui.IconMonitor, a.tr("settings.language.title"), a.tr("settings.language.desc"), func() {
			options := []string{a.tr("settings.language.system"), "简体中文", "English"}
			values := []string{"system", "zh-Hans", "en"}
			current := options[0]
			for i, value := range values {
				if a.settingsDraft.Language == value {
					current = options[i]
				}
			}
			if ui.Select(c, &current, options).Width(220).Changed() {
				for i, option := range options {
					if option == current {
						a.settingsDraft.Language = values[i]
						a.persistLanguage()
						break
					}
				}
				a.invalidate()
			}
		})

		settingsCard(c, appui.IconFolder, a.tr("Download location"), a.tr("Files are saved here unless a request chooses another folder."), func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, appui.IconFolder).FontSize(16).TextColor(t.TextMuted)
				ui.TextInput(c, &a.settingsDraft.DownloadDir).Grow(1).Placeholder(a.tr("Download folder"))
				if ui.Button(c, a.tr("Browse…")).Clicked() {
					go a.pickDownloadDir()
				}
			})
		})

		settingsCard(c, appui.IconRefresh, a.tr("Performance"), a.tr("Choose how many downloads or fragments may run in parallel."), func() {
			performanceSliderRow(c, a.tr("Downloads"), a.tr("Maximum concurrent downloads"), &a.maxConcurrentSlider, 1, 16)
			performanceSliderRow(c, a.tr("Fragments"), a.tr("Concurrent fragments per download"), &a.concurrentFragmentsSlider, 1, 32)
		})

		settingsCard(c, appui.IconDownload, a.tr("Media defaults"), a.tr("These options are applied to new download requests."), func() {
			ui.Checkbox(c, &a.settingsDraft.EmbedThumbnail, a.tr("Embed thumbnail"))
			ui.Checkbox(c, &a.settingsDraft.EmbedMetadata, a.tr("Embed metadata and chapters"))
			ui.Checkbox(c, &a.settingsDraft.MultiAudio, a.tr("Download multiple audio tracks when available"))
			ui.Checkbox(c, &a.settingsDraft.PreferVP9Video, a.tr("Prefer VP9 video in MKV or WebM"))
		})

		settingsCard(c, appui.IconVideo, a.tr("Subtitles"), a.tr("Use comma-separated yt-dlp language codes, such as en, ur, or all."), func() {
			ui.Checkbox(c, &a.settingsDraft.WriteSubtitles, a.tr("Write subtitles by default"))
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, a.tr("Languages")).FontSize(12).TextColor(t.TextMuted).Width(86)
				ui.TextInput(c, &a.settingsSubtitleLangs).Grow(1).Placeholder(a.tr("all, en, ur"))
			})
		})

		settingsCard(c, appui.IconLink, a.tr("Network"), a.tr("Optional proxy, browser cookies, and a per-download rate limit."), func() {
			settingsLabeledInput(c, a.tr("Proxy"), &a.settingsDraft.Proxy, a.tr("http://… or socks5://…"))
			settingsLabeledInput(c, a.tr("Cookies"), &a.settingsDraft.CookiesBrowser, a.tr("chrome, firefox, edge…"))
			settingsLabeledInput(c, a.tr("Rate limit"), &a.settingsRateLimit, a.tr("0 = unlimited (Kbps)"))
		})

		settingsCard(c, appui.IconVideo, a.tr("settings.aryplus.title"), a.tr("settings.aryplus.desc"), func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, a.tr("settings.aryplus.key")).FontSize(12).TextColor(t.TextMuted).Width(86)
				ui.TextInput(c, &a.settingsDraft.ARYPlusAPIKey).Grow(1).Password().Placeholder(a.tr("settings.aryplus.placeholder"))
			})
		})

		settingsCard(c, nil, a.tr("Custom headers"), a.tr("One header per line in the form Key: Value."), func() {
			ui.TextArea(c, &a.settingsHeaders).FillWidth().Height(86).Placeholder(a.tr("Accept-Language: en-US"))
		})

		settingsCard(c, appui.IconConverter, a.tr("Conversion"), a.tr("Defaults used by the local FFmpeg converter."), func() {
			ui.Checkbox(c, &a.settingsDraft.KeepOriginals, a.tr("Keep originals after conversion"))
		})

		settingsCard(c, appui.IconAbout, a.tr("settings.tools.title"), a.tr("settings.tools.desc"), func() {
			a.toolsCardContent(c)
		})

		ui.Row(c).FillWidth().Gap(8).Children(func() {
			if ui.Button(c, a.tr("Export settings")).Clicked() {
				settings := a.settingsDraftSnapshot()
				go a.exportSettings(settings)
			}
			if ui.Button(c, a.tr("Import settings")).Clicked() {
				go a.importSettings()
			}
			ui.Spacer(c).Grow(1)
			if ui.PrimaryButton(c, a.tr("Save settings")).Clicked() {
				draft := refinedSettingsDraft{
					settings:       a.settingsDraftSnapshot(),
					maxConcurrent:  strconv.Itoa(int(a.maxConcurrentSlider)),
					concurrentFrag: strconv.Itoa(int(a.concurrentFragmentsSlider)),
					rateLimit:      a.settingsRateLimit,
					headers:        a.settingsHeaders,
					subtitleLangs:  a.settingsSubtitleLangs,
				}
				saved, err := validateSettingsDraft(draft)
				if err != nil {
					a.showToast(a.tr("Could not save settings: ") + err.Error())
					return
				}
				if a.settingsRepo == nil {
					a.showToast(a.tr("Could not save settings: settings repository unavailable"))
					return
				}
				if err := a.settingsRepo.Save(saved); err != nil {
					a.showToast(a.tr("Could not save settings: ") + err.Error())
					return
				}
				if a.applySettings(saved) {
					a.refreshToolChecks()
				}
				a.settingsRateLimit = strconv.Itoa(saved.RateLimitKbps)
				a.settingsSubtitleLangs = strings.Join(saved.SubtitleLangs, ", ")
				if a.manager != nil {
					a.manager.SetMaxWorkers(saved.MaxConcurrent)
				}
				a.showToast(a.tr("Settings saved"))
			}
		})
	})
}

func (a *App) refinedAboutPage(c *ui.Context) {
	t := c.Theme()
	// Center the card vertically: 1 unit of space above, 2 below (matches reference).
	ui.Column(c).Grow(1).FillWidth().Children(func() {
		ui.Spacer(c).Grow(1)
		ui.Scroll(c).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Center().Padding(0, 16).Children(func() {
				// ── card ─────────────────────────────────────────────────────
				ui.Column(c).Width(620).Background(t.Surface).Border(1, t.Border).Radius(16).Padding(36, 40).Gap(14).AlignItems(ui.Center).Children(func() {

					// Logo
					ui.Box(c).Size(84, 84).Background(t.Accent).Radius(20).Center().Children(func() {
						ui.Icon(c, appui.IconDownload).FontSize(40).TextColor(ui.Color{R: 255, G: 255, B: 255, A: 255})
					})

					// App name
					ui.Text(c, a.tr("Video Downloader")).FontSize(28).Bold()

					// Version pill
					ui.Row(c).Background(t.SurfaceHover).Border(1, t.Border).Radius(999).Padding(5, 14).Children(func() {
						ui.Text(c, a.tr("v3.3.0 · Open-source media downloader")).FontSize(12).Bold().TextColor(t.TextMuted)
					})

					// Description
					ui.Text(c, a.tr("Download, convert, and organize media from YouTube, TikTok, Instagram, X, ARY Plus, and more. Native Go desktop app — fast, lightweight, no Electron.")).
						FontSize(14).TextColor(t.TextMuted).MaxWidth(460)

					// Divider
					ui.Box(c).FillWidth().Height(1).Background(t.Border)

					// Developer credit
					ui.Text(c, a.tr("Developed by Faizan Gondal")).FontSize(14).Bold()

					// Link buttons row (outlined style)
					ui.Row(c).Gap(8).Wrap().Justify(ui.Center).Children(func() {
						aboutLinkBtn(c, t, appui.IconLink, "GitHub", "https://github.com/fazi-gondal")
						aboutLinkBtn(c, t, appui.IconLink, "Website", "https://codewithfazi.vercel.app/")
						aboutLinkBtn(c, t, appui.IconVideo, "Reference app", "https://github.com/fazi-gondal/Video-Downloader")
						aboutLinkBtn(c, t, appui.IconDownload, "Repository", "https://github.com/fazi-gondal/video-downloader-mygo")
						aboutLinkBtn(c, t, appui.IconSettings, "Report issue", "https://github.com/fazi-gondal/video-downloader-mygo/issues")
					})

					// Tech stack label + chips
					ui.Text(c, a.tr("BUILT WITH")).FontSize(10).Bold().TextColor(t.TextMuted)
					ui.Row(c).Gap(8).Wrap().Justify(ui.Center).Children(func() {
						techChip(c, "Go")
						techChip(c, "MyGo")
						techChip(c, "yt-dlp")
						techChip(c, "FFmpeg")
					})

					// License line
					ui.Link(c, a.tr("© 2026 Faizan Gondal · MIT License"), "https://github.com/fazi-gondal/video-downloader-mygo/blob/main/LICENSE").FontSize(12)
				})
			})
		})
		ui.Spacer(c).Grow(2)
	})
}

// aboutLinkBtn renders an outlined button that opens a URL when clicked.
func aboutLinkBtn(c *ui.Context, t *ui.Theme, icon *ui.SVG, label, url string) {
	ui.Row(c.Key("abt-"+label)).
		Background(ui.Color{}).
		Border(1, t.Border).
		Radius(10).
		Padding(10, 16).
		Gap(6).
		AlignItems(ui.Center).
		Children(func() {
			ui.Icon(c, icon).FontSize(14).TextColor(t.TextMuted)
			ui.Link(c, label, url).FontSize(13)
		})
}

// settingsCard renders a glass surface card section. icon may be nil to omit the
// leading accent icon (matches the reference _section header style when provided).
func settingsCard(c *ui.Context, icon *ui.SVG, title, subtitle string, children func()) {
	t := c.Theme()
	ui.Column(c).FillWidth().Material(glass.Glass{Clarity: 0.2}).Border(1, t.Border).Radius(12).Padding(16, 18).Gap(10).Children(func() {
		// Header: icon (accent) + bold title on one row
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			if icon != nil {
				ui.Icon(c, icon).FontSize(18).TextColor(t.Accent)
			}
			ui.Text(c, title).FontSize(14).Bold()
		})
		if subtitle != "" {
			ui.Text(c, subtitle).FontSize(11).TextColor(t.TextMuted).MaxWidth(680)
		}
		children()
	})
}

func performanceSliderRow(c *ui.Context, label, accessibleLabel string, value *float64, min, max float64) {
	t := c.Theme()
	ui.Row(c).FillWidth().Material(glass.Glass{Clarity: 0.45}).Border(1, t.Border).Radius(10).Padding(10, 12).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(t.TextMuted).Width(86)
		ui.StepSlider(c, value, min, max, 1).Label(accessibleLabel).Grow(1)
		ui.Text(c, strconv.Itoa(int(*value))).FontSize(13).Bold().TextColor(t.Accent).Width(28)
	})
}

func settingsLabeledInput(c *ui.Context, label string, value *string, placeholder string) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(t.TextMuted).Width(86)
		ui.TextInput(c, value).Grow(1).Placeholder(placeholder)
	})
}

// themeCard renders an icon card for one theme mode (light / dark / system).
// Matches the reference _ThemeSelector: unselected cards have a surface fill
// and a 1px muted border; selected gets PRIMARY_CONTAINER-equivalent tint + 1.5px accent border.
func (a *App) themeCard(c *ui.Context, mode string, icon *ui.SVG, label string) {
	a.ensureSettingsDraft()
	t := c.Theme()
	selected := a.settingsDraft.ThemeMode == mode
	var borderWidth float32
	var borderColor, bgColor, iconColor ui.Color
	if selected {
		borderWidth = 2
		borderColor = t.Accent
		bgColor = ui.Color{R: t.Accent.R, G: t.Accent.G, B: t.Accent.B, A: 28}
		iconColor = t.Accent
	} else {
		borderWidth = 1
		borderColor = t.Border
		bgColor = t.SurfaceHover
		iconColor = t.TextMuted
	}
	var row ui.Element
	if selected {
		row = ui.Column(c.Key("theme-card-"+mode)).
			Grow(1).
			Background(bgColor).
			Border(borderWidth, borderColor).
			Radius(10).
			Padding(14, 10).
			Gap(6).
			AlignItems(ui.Center).
			Children(func() {
				ui.Icon(c, icon).FontSize(22).TextColor(iconColor)
				ui.Text(c, label).FontSize(13).Bold()
			})
	} else {
		row = ui.Column(c.Key("theme-card-"+mode)).
			Grow(1).
			Material(glass.Glass{Clarity: 0.3}).
			Border(borderWidth, borderColor).
			Radius(10).
			Padding(14, 10).
			Gap(6).
			AlignItems(ui.Center).
			Children(func() {
				ui.Icon(c, icon).FontSize(22).TextColor(iconColor)
				ui.Text(c, label).FontSize(13).Bold()
			})
	}
	if !selected && row.Clicked() {
		a.settingsDraft.ThemeMode = mode
		a.invalidate()
	}
}

func techChip(c *ui.Context, label string) {
	t := c.Theme()
	ui.Row(c).Background(t.Surface).Border(1, t.Border).Radius(999).Padding(4, 10).Children(func() {
		ui.Text(c, label).FontSize(11).TextColor(t.TextMuted)
	})
}

// toolsCardContent renders the path fields, health rows, and re-check action.
// It is kept separate so the settings snapshot can render the card alone.
func (a *App) toolsCardContent(c *ui.Context) {
	t := c.Theme()
	ui.Text(c, a.tr("settings.tools.pathsDesc")).FontSize(11).TextColor(t.TextMuted)
	settingsLabeledInput(c, a.tr("settings.tools.ytdlpPath"), &a.settingsDraft.YtDlpPath, a.tr("settings.tools.autoPath"))
	settingsLabeledInput(c, a.tr("settings.tools.ffmpegPath"), &a.settingsDraft.FFmpegPath, a.tr("settings.tools.autoPath"))
	if len(a.toolChecks) == 0 {
		ui.Text(c, a.tr("settings.tools.checking")).FontSize(11).TextColor(t.TextMuted)
	}
	for _, check := range a.toolChecks {
		a.toolStatusRow(c, check)
	}
	if ui.Button(c, a.tr("settings.tools.recheck")).Clicked() {
		// Apply the paths currently shown in the form before probing, so
		// "Check again" validates the value the user just typed rather than
		// the last saved value. Saving still persists it.
		a.appliedYtDlpPath = a.settingsDraft.YtDlpPath
		a.appliedFFmpegPath = a.settingsDraft.FFmpegPath
		tools.SetOverrides(a.settingsDraft.YtDlpPath, a.settingsDraft.FFmpegPath)
		a.refreshToolChecks()
	}
}

// toolStatusRow shows one dependency with its effective version, where it came
// from, and the bundled version when a newer system copy won.
func (a *App) toolStatusRow(c *ui.Context, check tools.Check) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(3).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).Children(func() {
			ui.Text(c, check.Name).FontSize(13).Bold().Width(70)
			ui.Column(c).Grow(1).Gap(1).Children(func() {
				version := check.Version
				if version == "" {
					version = "—"
				}
				source := a.toolSourceLabel(check.Source)
				label := version
				if source != "" {
					label = a.tr("settings.tools.versionSource", map[string]any{"version": version, "source": source})
				}
				ui.Text(c, label).FontSize(12).TextColor(t.TextMuted).Ellipsis("…")
				if check.Path != "" {
					ui.Text(c, check.Path).FontSize(11).TextColor(t.TextMuted).Ellipsis("…")
				}
				if check.BuiltinPath != "" && check.BuiltinVersion != "" && check.Path != check.BuiltinPath {
					ui.Text(c, a.tr("settings.tools.builtinVersion", map[string]any{"version": check.BuiltinVersion})).FontSize(11).TextColor(t.TextMuted)
				}
			})
			status, color := a.toolStatusLabel(check, t)
			ui.Row(c).Background(t.SurfaceHover).Border(1, t.Border).Radius(999).Padding(4, 10).Children(func() {
				ui.Text(c, status).FontSize(11).Bold().TextColor(color)
			})
		})
		if check.TimedOut {
			ui.Text(c, a.tr("settings.tools.timedOutDetail")).FontSize(11).TextColor(t.TextMuted)
		}
		if check.Name == "ffmpeg" && !check.OK && !check.TimedOut {
			hint := a.tr("settings.tools.ffmpegHint")
			if runtime.GOOS == "darwin" {
				hint = a.tr("settings.tools.ffmpegHintMac")
			}
			ui.Text(c, hint).FontSize(11).TextColor(t.TextMuted)
		}
		if check.OverridePath != "" && !check.OverrideOK {
			ui.Text(c, a.tr("settings.tools.overrideUnavailable", map[string]any{"path": check.OverridePath})).FontSize(11).TextColor(t.Danger)
		}
	})
}

// persistLanguage saves the language immediately: unlike the rest of the form
// it is expected to survive without pressing Save settings.
func (a *App) persistLanguage() {
	if a.settingsRepo == nil {
		return
	}
	// Load first: only the language is meant to be committed, not the rest of
	// the form, which may still hold half-edited values.
	settings, err := a.settingsRepo.Load()
	if err != nil {
		a.showToast(a.tr("settings.language.saveFailed") + err.Error())
		return
	}
	settings = languageOnlySettings(settings, a.settingsDraft.Language)
	if err := a.settingsRepo.Save(settings); err != nil {
		a.showToast(a.tr("settings.language.saveFailed") + err.Error())
		return
	}
	a.setSettings(settings)
}

// languageOnlySettings applies just the language, so saving it cannot commit
// other half-edited form values.
func languageOnlySettings(settings config.AppSettings, language string) config.AppSettings {
	settings.Language = language
	return settings
}

// refreshToolChecks probes the tools off the render thread; yt-dlp can take
// seconds to answer on a cold start. Each result updates the UI as soon as it
// arrives, and the running services adopt the resolved paths immediately;
// already-running processes keep their own copy.
func (a *App) refreshToolChecks() {
	a.toolCheckToken++
	token := a.toolCheckToken
	tools.ResetProbeCache()
	go func() {
		done := make(chan struct{}, 3)
		for _, name := range []string{"yt-dlp", "ffmpeg", "ffprobe"} {
			name := name
			go func() {
				check := tools.Probe(name)
				a.updateOnMain(func() {
					if a.toolCheckToken != token {
						return
					}
					a.setToolCheck(check)
					switch check.Name {
					case "ffmpeg":
						if a.ffmpeg != nil {
							if check.OK {
								a.ffmpeg.SetFFmpegPath(check.Path)
							} else if !check.TimedOut {
								a.ffmpeg.SetFFmpegPath("")
							}
						}
						if a.ytdlp != nil {
							if check.OK {
								a.ytdlp.SetFFmpegPath(check.Path)
							} else if !check.TimedOut {
								a.ytdlp.SetFFmpegPath("")
							}
						}
					case "ffprobe":
						if a.ffmpeg != nil {
							if check.OK {
								a.ffmpeg.SetFFprobePath(check.Path)
							} else if !check.TimedOut {
								a.ffmpeg.SetFFprobePath("")
							}
						}
					}
					a.invalidate()
				})
				done <- struct{}{}
			}()
		}
		for i := 0; i < 3; i++ {
			<-done
		}
		a.updateOnMain(func() {
			if a.toolCheckToken != token {
				return
			}
			ffmpegPath := ""
			if a.ffmpeg != nil {
				a.ffmpeg.RefreshTools()
				ffmpegPath = a.ffmpeg.Path()
			}
			if a.ytdlp != nil {
				a.ytdlp.RefreshPaths(ffmpegPath)
			}
			a.invalidate()
		})
	}()
}
