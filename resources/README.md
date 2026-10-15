# Resources and bundled tools

This directory contains shared application assets and optional platform-specific helper executables. MyGo copies the resources that match the target platform into the packaged application.

## Directory layout

```text
resources/
├── README.md
├── icon.png
├── windows-amd64/
│   └── bin/
│       ├── yt-dlp.exe
│       ├── ffmpeg.exe
│       └── ffprobe.exe
├── windows-arm64/
│   └── bin/
│       ├── yt-dlp.exe
│       ├── ffmpeg.exe
│       └── ffprobe.exe
├── linux-amd64/
│   └── bin/
│       ├── yt-dlp
│       ├── ffmpeg
│       └── ffprobe
├── linux-arm64/
│   └── bin/
│       ├── yt-dlp
│       ├── ffmpeg
│       └── ffprobe
├── darwin-amd64/
│   └── bin/
│       ├── yt-dlp
│       ├── ffmpeg
│       └── ffprobe
└── darwin-arm64/
    └── bin/
        ├── yt-dlp
        ├── ffmpeg
        └── ffprobe
```

The `bin` directories are usually empty in Git because downloaded executables are ignored. The release workflow populates the matching directory before running `go tool mygo build`.

`icon.png` is shared by application packages. Use a 1024×1024 PNG when replacing it.

## Fetch tools

Run the Bash helper from the repository root:

```bash
# Detect the current host platform.

# Download one target explicitly.
bash scripts/fetch-tools.sh linux-amd64

# Populate all supported release targets.
```

The helper downloads:

- yt-dlp release binaries from GitHub releases.
- Static FFmpeg/FFprobe archives from `yt-dlp/FFmpeg-Builds` for Windows/Linux.
- Architecture-matched FFmpeg/FFprobe from a verified Homebrew bottle for macOS.

The helper requires `curl`, `unzip`, and `tar`/`xz` for the archive type. On Windows, run it from Git Bash or WSL; do not run this Bash script with Windows PowerShell directly.

## Version controls

The script supports:

```bash
YTDLP_TAG=2026.08.19
FFMPEG_RELEASE=latest
```

Example:

```bash
YTDLP_TAG=2026.08.19 FFMPEG_RELEASE=latest \
  bash scripts/fetch-tools.sh linux-amd64
```

The Windows/Linux archive checksums are recorded in the script. The `latest` FFmpeg archive is accepted only while its recorded checksum matches; update the release name and checksum together when intentionally upgrading it. Homebrew verifies macOS bottle checksums as part of `brew install`.

## Runtime lookup order

`internal/tools/resolve.go` resolves each executable in this order:

1. `App.Path(PathResources)/bin/<tool>` in a packaged app.
2. `App.Path(PathResources)/<tool>` in a packaged app.
3. `resources/<GOOS>-<GOARCH>/bin/<tool>` during local development.
4. A matching executable on the system `PATH`.

Tool names are:

| Tool | Used for |
| --- | --- |
| `yt-dlp` | URL analysis, format discovery, media downloads, subtitles, and metadata. |
| `ffmpeg` | Stream merging, MKV remuxing, subtitle embedding, thumbnail/metadata post-processing, and conversion. |
| `ffprobe` | Media probing and duration information used by conversion. |

On Windows, `.exe` is added automatically. On Unix-like systems, downloaded files are marked executable by the helper script.

## Packaging behavior

MyGo selects platform-named resource directories during packaging. A Windows package receives Windows resources; a Linux package receives Linux resources. Do not copy every platform's binaries into one flat directory because that can cause the wrong executable to be shipped.

The release workflow downloads the helpers immediately before packaging and uploads the resulting installers and archives. Local `go run` uses the platform-directory fallback, so local development can use the same layout without flattening it manually. macOS helper downloads must run on a runner or machine with the target architecture.

## Repository policy

- Do not commit FFmpeg archives or extracted binaries.
- Do not store cookies, API keys, proxy credentials, or private URLs in this directory.
- Keep shared icons and non-secret static resources in the top-level `resources` directory.
- Update `BUILD.md` and the root `README.md` when resource layout or packaging behavior changes.
