package ui

import "github.com/egoist/mygo/ui"

// Nocturnal Studio palette (dark).
var (
	ColorWindowBg     = ui.Color{R: 0x11, G: 0x13, B: 0x18, A: 0xff}
	ColorSurfaceBase  = ui.Color{R: 0x16, G: 0x18, B: 0x1d, A: 0xff}
	ColorSurfaceElev  = ui.Color{R: 0x1e, G: 0x21, B: 0x27, A: 0xff}
	ColorSurfaceHigh  = ui.Color{R: 0x2a, G: 0x2d, B: 0x35, A: 0xff}
	ColorBorderSubtle = ui.Color{R: 0xff, G: 0xff, B: 0xff, A: 0x14} // ~8%
	ColorBorderStrong = ui.Color{R: 0xff, G: 0xff, B: 0xff, A: 0x26} // ~15%
	ColorText         = ui.Color{R: 0xe2, G: 0xe2, B: 0xe9, A: 0xff}
	ColorTextMuted    = ui.Color{R: 0x9b, G: 0xa1, B: 0xa6, A: 0xff}
	ColorAccent       = ui.Color{R: 0xff, G: 0x5c, B: 0x49, A: 0xff} // coral
	ColorAccentHover  = ui.Color{R: 0xff, G: 0x72, B: 0x61, A: 0xff}
	ColorTeal         = ui.Color{R: 0x32, G: 0xa0, B: 0x97, A: 0xff}
	ColorSuccess      = ui.Color{R: 0x4c, G: 0xaf, B: 0x50, A: 0xff}
	ColorError        = ui.Color{R: 0xef, G: 0x53, B: 0x50, A: 0xff}
	ColorWarning      = ui.Color{R: 0xff, G: 0x98, B: 0x00, A: 0xff}
)

// NativeTheme returns the mygo theme used by the Go port. It keeps the
// reference app's Nocturnal Studio palette while retaining mygo's native
// controls, focus behavior, and system light/dark selection.
func NativeTheme(dark bool) *ui.Theme {
	if dark {
		t := ui.DarkTheme()
		t.Background = ColorWindowBg
		t.Surface = ColorSurfaceElev
		t.SurfaceHover = ColorSurfaceHigh
		t.SurfacePressed = ui.Hex("#353944")
		t.Border = ColorBorderStrong
		t.Text = ColorText
		t.TextMuted = ColorTextMuted
		t.Accent = ColorAccent
		t.AccentHover = ColorAccentHover
		t.AccentPressed = ui.Hex("#e84a38")
		t.AccentText = ui.Hex("#ffffff")
		t.Danger = ColorError
		t.Warning = ColorWarning
		t.Success = ColorSuccess
		t.Selection = ui.RGBA(255, 92, 73, 0.28)
		t.Focus = ui.RGBA(255, 114, 97, 0.62)
		t.Scrollbar = ui.RGBA(255, 255, 255, 0.28)
		t.Radius = 10
		t.Spacing = 4
		t.FontSize = 14
		return t
	}

	t := ui.LightTheme()
	t.Background = ui.Hex("#f8f9ff")
	t.Surface = ui.Hex("#ffffff")
	t.SurfaceHover = ui.Hex("#f0f1f6")
	t.SurfacePressed = ui.Hex("#e5e6ec")
	t.Border = ui.Hex("#d9dbe3")
	t.Text = ui.Hex("#1a1c1e")
	t.TextMuted = ui.Hex("#5f636b")
	t.Accent = ColorAccent
	t.AccentHover = ui.Hex("#e84a38")
	t.AccentPressed = ui.Hex("#cf3e2f")
	t.AccentText = ui.Hex("#ffffff")
	t.Danger = ui.Hex("#c62828")
	t.Warning = ui.Hex("#b26a00")
	t.Success = ui.Hex("#2e7d32")
	t.Selection = ui.RGBA(255, 92, 73, 0.22)
	t.Focus = ui.RGBA(232, 74, 56, 0.42)
	t.Scrollbar = ui.RGBA(0, 0, 0, 0.25)
	t.Radius = 10
	t.Spacing = 4
	t.FontSize = 14
	return t
}
