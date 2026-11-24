# Resources and bundled tools

This directory contains shared application assets and optional platform-specific helper executables. MyGo copies the resources that match the target platform into the packaged application.

The application release is currently `v3.3.0`. The application version is defined
in the repository root `mygo.json`; the helper versions below are independent and
must be changed only when the bundled toolchain is intentionally upgraded.

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
│       └── yt-dlp
└── darwin-arm64/
    └── bin/
        └── yt-dlp
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
- Nothing for macOS FFmpeg: the app uses the FFmpeg installed on the machine,
  or the path set in Settings → Tools.

The helper requires `curl`, `unzip`, and `tar`/`xz` for the archive type. On Windows, run it from Git Bash or WSL; do not run this Bash script with Windows PowerShell directly.

## Version controls

`mygo.json` controls the Video Downloader application version. The variables in
this section control only the bundled yt-dlp and FFmpeg helper versions.

The script supports:

```bash
YTDLP_TAG=2026.11.23
FFMPEG_RELEASE=latest
```

Example:

```bash
YTDLP_TAG=2026.11.23 FFMPEG_RELEASE=latest \
  bash scripts/fetch-tools.sh linux-amd64
```

The Windows/Linux archive checksums are recorded in the script. The `latest` FFmpeg archive is accepted only while its recorded checksum matches; update the release name and checksum together when intentionally upgrading it. macOS has no FFmpeg download to verify.

## Runtime lookup order

`internal/tools/resolve.go` resolves each executable in this order:

1. An explicit path from Settings → Tools, if it runs.
2. For yt-dlp, the newer of the bundled and system copies.
3. `App.Path(PathResources)/bin/<tool>` in a packaged app.
4. `App.Path(PathResources)/<tool>` in a packaged app.
5. `resources/<GOOS>-<GOARCH>/bin/<tool>` during local development.
6. A matching executable in the common package-manager directories or on the
   system `PATH`.

Every candidate is probed by running it once, so a present-but-broken binary is
skipped. macOS packages contain only yt-dlp; FFmpeg and FFprobe come from the
system or an explicit path.

Tool names are:

| Tool | Used for |
| --- | --- |
| `yt-dlp` | URL analysis, format discovery, media downloads, subtitles, and metadata. |
| `ffmpeg` | Stream merging, MKV remuxing, subtitle embedding, thumbnail/metadata post-processing, and conversion. |
| `ffprobe` | Media probing and duration information used by conversion. |

## ARY Plus support

The application includes native Go support for ARY Plus (`aryplus.tv`) episode URLs
and series playlists, including the YouTube and Dailymotion platform redirects.
Episode metadata, series episodes, and direct HLS stream sources are resolved
directly via Go HTTP client requests using the built-in public client key.
`ARYPLUS_API_KEY` can override the key and `ARYPLUS_API_BASE` can select a
compatible API endpoint. Direct HLS streams are downloaded and merged by the
bundled tools without requiring any external plugins.

On Windows, `.exe` is added automatically. On Unix-like systems, downloaded files are marked executable by the helper script.

## Conversion support

The bundled FFmpeg and FFprobe binaries power the in-app Converter as well as
download post-processing when present. On macOS the app uses the system FFmpeg
instead. Lossy audio targets expose 128, 192, 256, and 320 kbps; FLAC and WAV
remain lossless and do not receive a bitrate flag. Video conversion chooses
codecs that match the target container and uses a remux only after FFprobe
confirms that the source codecs are compatible.

## Packaging behavior

MyGo selects platform-named resource directories during packaging. A Windows package receives Windows resources; a Linux package receives Linux resources. Do not copy every platform's binaries into one flat directory because that can cause the wrong executable to be shipped.

The release workflow downloads the helpers immediately before packaging and uploads the resulting installers and archives. Local `go run` uses the platform-directory fallback, so local development can use the same layout without flattening it manually. macOS packaging never downloads FFmpeg; it ships yt-dlp only and resolves FFmpeg on the user's machine.

## Repository policy

- Do not commit FFmpeg archives or extracted binaries.
- Do not store cookies, API keys, proxy credentials, or private URLs in this directory.
- Keep shared icons and non-secret static resources in the top-level `resources` directory.
- Update `BUILD.md` and the root `README.md` when resource layout or packaging behavior changes.
