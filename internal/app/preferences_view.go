package app

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"github.com/fazi-gondal/video-downloader-mygo/internal/config"
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
		ui.Column(c).Gap(3).Children(func() {
			ui.Text(c, "Settings").FontSize(20).Bold()
			ui.Text(c, "Tune downloads, media defaults, and the appearance of MyGo.").FontSize(12).TextColor(t.TextMuted)
		})

		settingsCard(c, "Appearance", "Choose how MyGo follows your system theme.", func() {
			ui.Row(c).Gap(8).Wrap().Children(func() {
				a.themeChoice(c, "system", "System")
				a.themeChoice(c, "dark", "Dark")
				a.themeChoice(c, "light", "Light")
			})
		})

		settingsCard(c, "Download location", "Files are saved here unless a request chooses another folder.", func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, appui.IconFolder).FontSize(16).TextColor(t.TextMuted)
				ui.TextInput(c, &a.settingsDraft.DownloadDir).Grow(1).Placeholder("Download folder")
				if ui.Button(c, "Browse…").Clicked() {
					go a.pickDownloadDir()
				}
			})
		})

		settingsCard(c, "Performance", "Choose how many downloads or fragments may run in parallel.", func() {
			performanceSliderRow(c, "Downloads", "Maximum concurrent downloads", &a.maxConcurrentSlider, 1, 16)
			performanceSliderRow(c, "Fragments", "Concurrent fragments per download", &a.concurrentFragmentsSlider, 1, 32)
		})

		settingsCard(c, "Media defaults", "These options are applied to new download requests.", func() {
			ui.Checkbox(c, &a.settingsDraft.EmbedThumbnail, "Embed thumbnail")
			ui.Checkbox(c, &a.settingsDraft.EmbedMetadata, "Embed metadata and chapters")
			ui.Checkbox(c, &a.settingsDraft.MultiAudio, "Download multiple audio tracks when available")
			ui.Checkbox(c, &a.settingsDraft.PreferVP9Video, "Prefer VP9 video when available (fallback automatically)")
		})

		settingsCard(c, "Subtitles", "Use comma-separated yt-dlp language codes, such as en, ur, or all.", func() {
			ui.Checkbox(c, &a.settingsDraft.WriteSubtitles, "Write subtitles by default")
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Languages").FontSize(12).TextColor(t.TextMuted).Width(86)
				ui.TextInput(c, &a.settingsSubtitleLangs).Grow(1).Placeholder("all, en, ur")
			})
		})

		settingsCard(c, "Network", "Optional proxy, browser cookies, and a per-download rate limit.", func() {
			settingsLabeledInput(c, "Proxy", &a.settingsDraft.Proxy, "http://… or socks5://…")
			settingsLabeledInput(c, "Cookies", &a.settingsDraft.CookiesBrowser, "chrome, firefox, edge…")
			settingsLabeledInput(c, "Rate limit", &a.settingsRateLimit, "0 = unlimited (Kbps)")
		})

		settingsCard(c, "Custom headers", "One header per line in the form Key: Value.", func() {
			ui.TextArea(c, &a.settingsHeaders).FillWidth().Height(86).Placeholder("Accept-Language: en-US")
		})

		settingsCard(c, "Conversion", "Defaults used by the local FFmpeg converter.", func() {
			ui.Checkbox(c, &a.settingsDraft.KeepOriginals, "Keep originals after conversion")
		})

		settingsCard(c, "Dependencies", "The executable paths currently resolved by the app.", func() {
			dependencyRow(c, "FFmpeg", dependencyPath(a.ffmpegPath()))
			dependencyRow(c, "yt-dlp", dependencyPath(a.ytdlpPath()))
		})

		ui.Row(c).FillWidth().Gap(8).Children(func() {
			if ui.Button(c, "Export settings").Clicked() {
				settings := a.settingsDraftSnapshot()
				go a.exportSettings(settings)
			}
			if ui.Button(c, "Import settings").Clicked() {
				go a.importSettings()
			}
			ui.Spacer(c).Grow(1)
			if ui.PrimaryButton(c, "Save settings").Clicked() {
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
					a.showToast("Could not save settings: " + err.Error())
					return
				}
				if a.settingsRepo == nil {
					a.showToast("Could not save settings: settings repository unavailable")
					return
				}
				if err := a.settingsRepo.Save(saved); err != nil {
					a.showToast("Could not save settings: " + err.Error())
					return
				}
				a.applySettings(saved)
				a.settingsRateLimit = strconv.Itoa(saved.RateLimitKbps)
				a.settingsSubtitleLangs = strings.Join(saved.SubtitleLangs, ", ")
				if a.manager != nil {
					a.manager.SetMaxWorkers(saved.MaxConcurrent)
				}
				a.showToast("Settings saved")
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
					ui.Text(c, "Video Downloader").FontSize(28).Bold()

					// Version pill
					ui.Row(c).Background(t.SurfaceHover).Border(1, t.Border).Radius(999).Padding(5, 14).Children(func() {
						ui.Text(c, "v3.0.0 · Open-source media downloader").FontSize(12).Bold().TextColor(t.TextMuted)
					})

					// Description
					ui.Text(c, "Download, convert, and organize media from YouTube, TikTok, Instagram, X, ARY Plus, and more. Native Go desktop app — fast, lightweight, no Electron.").
						FontSize(14).TextColor(t.TextMuted).MaxWidth(460)

					// Divider
					ui.Box(c).FillWidth().Height(1).Background(t.Border)

					// Developer credit
					ui.Text(c, "Developed by Faizan Gondal").FontSize(14).Bold()

					// Link buttons row (outlined style)
					ui.Row(c).Gap(8).Wrap().Justify(ui.Center).Children(func() {
						aboutLinkBtn(c, t, appui.IconLink, "GitHub", "https://github.com/fazi-gondal")
						aboutLinkBtn(c, t, appui.IconLink, "Website", "https://codewithfazi.vercel.app/")
						aboutLinkBtn(c, t, appui.IconVideo, "Reference app", "https://github.com/fazi-gondal/Video-Downloader")
						aboutLinkBtn(c, t, appui.IconDownload, "Repository", "https://github.com/fazi-gondal/video-downloader-mygo")
						aboutLinkBtn(c, t, appui.IconSettings, "Report issue", "https://github.com/fazi-gondal/video-downloader-mygo/issues")
					})

					// Tech stack label + chips
					ui.Text(c, "BUILT WITH").FontSize(10).Bold().TextColor(t.TextMuted)
					ui.Row(c).Gap(8).Wrap().Justify(ui.Center).Children(func() {
						techChip(c, "Go")
						techChip(c, "MyGo")
						techChip(c, "yt-dlp")
						techChip(c, "FFmpeg")
					})

					// License line
					ui.Link(c, "© 2026 Faizan Gondal · MIT License", "https://github.com/fazi-gondal/video-downloader-mygo/blob/main/LICENSE").FontSize(12)
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

func settingsCard(c *ui.Context, title, subtitle string, children func()) {
	t := c.Theme()
	ui.Column(c).FillWidth().Material(glass.Glass{Clarity: 0.2}).Border(1, t.Border).Radius(12).Padding(14, 16).Gap(9).Children(func() {
		ui.Text(c, title).FontSize(14).Bold()
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

func dependencyRow(c *ui.Context, label, path string) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(t.TextMuted).Width(86)
		ui.Text(c, path).FontSize(12).Ellipsis("…").Grow(1)
	})
}

func dependencyPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return "Not found"
	}
	return path
}

func (a *App) ffmpegPath() string {
	if a.ffmpeg == nil {
		return ""
	}
	return a.ffmpeg.Path()
}

func (a *App) ytdlpPath() string {
	if a.ytdlp == nil {
		return ""
	}
	return a.ytdlp.Bin()
}

func (a *App) themeChoice(c *ui.Context, mode, label string) {
	a.ensureSettingsDraft()
	selected := a.settingsDraft.ThemeMode == mode
	if selected {
		ui.PrimaryButton(c.Key("theme-"+mode+"-selected"), label)
		return
	}
	if ui.Button(c.Key("theme-"+mode), label).Clicked() {
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
