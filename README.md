# Video Downloader (MyGo Native UI)

Rewrite of [fazi-gondal/Video-Downloader](https://github.com/fazi-gondal/Video-Downloader) using **MyGo native UI** (GPU-drawn, no webview).

## Features

- **Native OS title bar** (Windows caption buttons / macOS traffic lights / Linux decorations) — not frameless
- Sidebar navigation: Dashboard, Downloads, Converter, History, Settings, About
- URL analysis (single + playlist) via yt-dlp
- **Format picker** with resolution / codec / size listing + explicit format ID override
- Multi-audio track selection → mkv multi-stream
- Subtitle embed toggle
- Concurrent download queue with cancel / retry / live progress
- **Playlist batch progress** in the top bar
- **Media converter** (FFmpeg) with **Browse…** file dialog + drag-and-drop
- **Download folder** picker in Settings
- **Show in folder** on completed downloads
- **History** (JSON persistence of completed/failed downloads)
- Settings: download dir, concurrency, proxy, cookies browser, embed options

## Requirements

- Go 1.22+
- `yt-dlp` on PATH
- `ffmpeg` / `ffprobe` on PATH (for merge + converter)
- MyGo: `go get github.com/egoist/mygo@latest`

## Run

```bash
cd video-downloader-mygo
go get github.com/egoist/mygo@latest
go mod tidy
go run .
```

## Architecture

```
internal/
  app/          # root View + all pages (MyGo ui)
  config/       # settings JSON
  models/       # DownloadTask, MediaInfo, ConversionJob
  services/
    ytdlp.go           # analyze / formats / download
    download_manager.go
    ffmpeg.go          # convert + probe
    conversion.go      # conversion queue
    history.go         # JSON history
```

Window uses default chrome (`WindowOptions` without `Frameless` / `TitleBarHidden`).

## Notes on MyGo APIs used

- `ui.Sidebar` / `SidebarSection` / `SidebarItem`
- `ui.TextInput(c, *string)`, `ui.Checkbox(c, *bool, label)`, `ui.Slider(c, *float64, lo, hi)`
- `ui.Progress(c, 0..1)` (negative = indeterminate)
- `ui.List(c, *ListState, n, rowFn)`
- `ui.Button` / `PrimaryButton`, `ui.Spinner`
- Theme via `c.Theme()` (`Surface`, `Background`, `Accent`, `TextMuted`, `Danger`, `Success`)

If a widget signature drifts with a newer MyGo, check https://mygo.egoist.dev/docs/ui .


See [BUILD.md](BUILD.md) for packaging with `mygo build`.
