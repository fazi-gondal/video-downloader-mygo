# Building & packaging

Native-UI MyGo app — no Bun/frontend required.

## Develop

```bash
go get github.com/egoist/mygo@latest
go mod tidy

# option A: plain Go
go run .

# option B: MyGo CLI (live rebuild on Go changes)
go run github.com/egoist/mygo/cmd/mygo@latest dev
# or after: go install github.com/egoist/mygo/cmd/mygo@latest
mygo dev
```

## Production build (this machine)

```bash
go run github.com/egoist/mygo/cmd/mygo@latest build
# → build/<os>-<arch>/
```

## Cross-compile targets

```bash
# Windows installer (amd64) — run on any OS; NSIS packaging needs Windows or wine for full installer
mygo build -platform windows/amd64

# macOS universal (needs Mac for signing/DMG)
mygo build -platform darwin/universal

# Linux
mygo build -platform linux/amd64,linux/arm64
```

Outputs land in `build/` (see `mygo.json` → `out`).

Typical artifacts:
- **Windows**: `Video Downloader Setup <ver> amd64.exe` (NSIS) + portable dir
- **macOS**: `.app` + `.dmg`
- **Linux**: directory + optional `.deb` when configured

## Icon

Put a **1024×1024 PNG** at `resources/icon.png`. MyGo embeds it into the OS packages.

## Runtime dependencies

Ship or document:
- `yt-dlp` on PATH (or bundle a binary next to the app and set PATH in a launcher)
- `ffmpeg` / `ffprobe` on PATH for merges and the Converter page

## Optional: pin CLI as a Go tool

```bash
go get -tool github.com/egoist/mygo/cmd/mygo@latest
go tool mygo build
```

## Signing (optional)

- **macOS**: set `macos.signingIdentity` in `mygo.json` or `mygo build -sign "Developer ID Application: …"`
- **Windows**: set `windows.certificate` to a `.pfx` path; password via `MYGO_WINDOWS_CERTIFICATE_PASSWORD`

## Bundled yt-dlp & FFmpeg

```bash
# Download tools for this OS into resources/<plat>/bin/
./scripts/fetch-tools.sh

# Or every platform (release packaging)
./scripts/fetch-tools.sh all
```

Resolution order at runtime (`internal/tools`):

1. `resources/…/bin/yt-dlp` (or `.exe`) next to the packaged app  
2. system `PATH`

Platform folders follow MyGo conventions (`windows-amd64`, `linux-amd64`, …).  
`mygo build` only packs the folder matching the target.

GitHub Actions (`.github/workflows/release.yml`) runs `fetch-tools.sh` before each platform build and uploads installers/archives to a **draft** release on `v*` tags.

```bash
# bump version in mygo.json first, then:
git tag v2.0.0
git push origin v2.0.0
```
