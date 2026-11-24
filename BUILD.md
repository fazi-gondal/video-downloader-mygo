# Build, package, and release

This repository is a native-UI MyGo application. It has no Bun, Vite, or web frontend build. The MyGo CLI is pinned in `go.mod` and must be invoked with `go tool mygo` so local and CI packaging use the same version as the application library.

## Prerequisites

- Go 1.27.3, as declared in `go.mod`.
- Git.
- MyGo v0.10.0 through the pinned Go tool in `go.mod`.
- yt-dlp and, on Windows/Linux, FFmpeg/FFprobe on `PATH`, or downloaded into
  `resources/<platform>/bin`. On macOS, FFmpeg is not bundled: install it with
  Homebrew, MacPorts, or a static build, or set its path in Settings → Tools.
- ARY Plus uses the built-in public client key. `ARYPLUS_API_KEY` can override
  it at runtime, and `ARYPLUS_API_BASE` can select a compatible API endpoint.
- TikTok, Instagram, X/Twitter, and YouTube Shorts downloads do not require a
  cookies file or browser-cookie configuration. The direct flow uses yt-dlp's
  best available video/audio selection and does not expose manual quality
  controls. Final filenames use the media title without an appended video ID.
- Bash, curl, unzip, and tar/xz when using `scripts/fetch-tools.sh`.
- On Windows, Git Bash or WSL is required to run the Bash helper script. The Go application itself runs natively from PowerShell or Command Prompt.

MyGo does not require cgo for normal native builds. The CI and release workflows set `CGO_ENABLED=0` for reproducible packaging.

## Development

From the repository root:

```bash
go mod download
go run .
```

MyGo development mode rebuilds the native app when Go files change:

```bash
go tool mygo dev
```

Do not use `go get github.com/egoist/mygo@latest` as a routine setup command. It can rewrite the module graph away from the version pinned by this project.

## Verification commands

```bash
gofmt -w <changed-go-files>
go test ./... -count=1
go vet ./...
go build ./...
git diff --check
```

Run `go test -race ./...` when a C compiler is available. Race testing is optional for local Windows environments without GCC; it is not required for the production MyGo build because production packaging uses `CGO_ENABLED=0`.

## Download bundled helper tools

The application can use system tools, but release packages include a
platform-specific yt-dlp and, on Windows/Linux, static FFmpeg binaries. macOS
deliberately ships only yt-dlp so the installer stays small and a broken
bundled dylib cannot shadow a working system copy. Run the helper with Bash:

```bash
# Current host platform.
bash scripts/fetch-tools.sh

# One explicit target.
bash scripts/fetch-tools.sh linux-amd64

# All supported helper directories used by release packaging.
bash scripts/fetch-tools.sh all
```

The script writes to:

```text
resources/<platform>/bin/
```

Supported target names include `windows-amd64`, `windows-arm64`, `linux-amd64`, `linux-arm64`, `darwin-amd64`, and `darwin-arm64`. Every FFmpeg archive is verified against the release's published checksum, with the pinned fallback values in the script. macOS downloads yt-dlp only; FFmpeg is selected from the user's machine at runtime.

Optional environment variables:

```bash
YTDLP_TAG=2026.11.23 FFMPEG_RELEASE=latest \
  bash scripts/fetch-tools.sh linux-amd64
```

The default yt-dlp version and SHA-256 checksums are recorded in the script.
The FFmpeg rolling release is accepted only when its published checksum (or the
pinned fallback) matches; update the release name and checksum together when
intentionally upgrading it. Do not commit the downloaded files: `resources/*/bin/*` is
ignored because FFmpeg binaries are large.

The shared `resources/yt-dlp-plugins` directory is packaged with the
application and loaded by yt-dlp through `--plugin-dirs`. Each child is a
standard yt-dlp plugin package; this is how the ARY Plus extractor is available
in local and packaged runs.

## Local production builds

Build the default target:

```bash
CGO_ENABLED=0 go tool mygo build
```

Build explicit targets:

```bash
CGO_ENABLED=0 go tool mygo build -platform windows/amd64
CGO_ENABLED=0 go tool mygo build -platform linux/amd64
CGO_ENABLED=0 go tool mygo build -platform linux/arm64
CGO_ENABLED=0 go tool mygo build -platform darwin/arm64
```

MyGo can cross-compile native UI targets without cgo. macOS signing and disk-image creation require a macOS environment and its signing tools. Windows installer creation from Linux requires NSIS; the release workflow installs it on the Windows build row.

Outputs are written below `build/`, as configured by `mygo.json`:

- Windows: executable and `Video Downloader Setup <version> <arch>.exe`.
- Linux: executable, `.deb`, `.tar.gz`, and `install.sh`.
- macOS: `.app` and `.dmg` when built on macOS.

## Resource resolution

`internal/tools` checks, in order:

1. Explicit path from Settings → Tools, when it runs.
2. For yt-dlp, the newer of the bundled and system copies (Homebrew/MacPorts/
   `PATH`); a user-installed update takes effect without rebuilding the app.
3. Packaged `resources/bin/<tool>` or `resources/<tool>` (development
   `resources/<os>-<arch>/bin/<tool>` is also recognized).
4. System `PATH` and the common package-manager directories.

Every candidate is executed once (`-version` or `--version`) and cached, so a
present-but-broken binary falls through instead of shadowing a working copy.
The Settings Tools card shows the effective path, the bundled version when it
differs, and a warning when a custom path does not run.

The platform directories are flattened/selected by MyGo during packaging. During `go run`, the platform subdirectory remains in the repository, which is why the development lookup exists.

## Application metadata

`mygo.json` currently defines:

- Application name: `Video Downloader`.
- Identifier: `com.fazigondal.videodownloader`.
- Version: `3.3.0`.
- Output directory: `build`.
- Linux maintainer metadata.

When publishing a release, the Git tag must match the version: `mygo.json` version `3.3.0` requires tag `v3.3.0`.

## Version 3.3.0 release checklist

Before creating the release tag:

1. Confirm `mygo.json` contains version `3.3.0`.
2. Confirm the About page displays `v3.3.0`.
3. Update `CHANGELOG.md` with the release date and user-visible changes.
4. Run the verification commands in this document and the packaging smoke builds.
5. Review the working tree so downloaded helper binaries, credentials, cookies, and local settings are not included.
6. Create and push the matching tag:

```bash
git tag v3.3.0
git push origin v3.3.0
```

The release workflow validates the tag against `mygo.json`, creates a draft GitHub Release, builds the supported packages, uploads the artifacts, and publishes the release only after all platform jobs succeed.

## Signing

Signing is optional for local development and not configured by default.

### macOS

Set a Developer ID identity in `mygo.json` or pass the MyGo signing flag:

```bash
CGO_ENABLED=0 go tool mygo build -sign "Developer ID Application: …"
```

See the MyGo [GitHub Actions guide](https://mygo.egoist.dev/docs/github-actions) for notarization credentials and keychain setup.

### Windows

Configure a `.pfx` certificate through the MyGo Windows configuration and provide the certificate password through `MYGO_WINDOWS_CERTIFICATE_PASSWORD`. Unsigned installers may trigger SmartScreen warnings.

## GitHub Actions

### CI

`.github/workflows/ci.yml` runs on pushes to `main`/`master` and pull requests. It uses the Go version from `go.mod`, verifies the committed dependency graph, runs the race-enabled test suite and vet, performs a Linux MyGo packaging smoke build, and validates the Windows, Linux, and macOS arm64 packaging matrix.

### Release

`.github/workflows/release.yml` runs on `v*` tags. It:

1. Checks that the tag matches `mygo.json`.
2. Creates a draft GitHub Release.
3. Fetches verified helper tools for Windows/Linux; macOS fetches yt-dlp only.
4. Builds Windows and Linux amd64/arm64 plus macOS arm64 with `go tool mygo` and `CGO_ENABLED=0`.
5. Uploads Windows installers, Linux packages/archives/install script, and macOS disk images to the draft.
6. Publishes the release only after every platform job succeeds.

Required workflow permission:

```yaml
permissions:
  contents: write
```

Optional update/signing secrets are described in the MyGo documentation and the comments at the top of the release workflow.

## Icon and resources

Place a shared 1024×1024 PNG at `resources/icon.png`. Platform-specific helper binaries belong in `resources/<platform>/bin`. See [resources/README.md](resources/README.md) for the complete layout.

## Common build failures

### `mygo: command not found`

Use `go tool mygo ...`; do not depend on a user-specific Go bin directory being on `PATH`.

### Installer is not generated

Windows installer creation needs NSIS. The release workflow installs NSIS on Ubuntu for the Windows target. A plain cross-build may still produce the executable without a complete installer if NSIS is unavailable.

### FFmpeg or yt-dlp is missing

Run the helper script using Bash, check `resources/<platform>/bin`, install the
tools on `PATH`, or set an explicit path in Settings → Tools. On macOS install
FFmpeg with `brew install ffmpeg` (or a static build) and press **Check again**.
Restart the app after changing bundled resources.

### Linux executable does not start

Install the MyGo Linux runtime libraries for the distribution. Prefer the `.deb` package when available.
