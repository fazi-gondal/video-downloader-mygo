# Build, package, and release

This repository is a native-UI MyGo application. It has no Bun, Vite, or web frontend build. The MyGo CLI is pinned in `go.mod` and must be invoked with `go tool mygo` so local and CI packaging use the same version as the application library.

## Prerequisites

- Go 1.27.1, as declared in `go.mod`.
- Git.
- MyGo v0.5.1 through the pinned Go tool in `go.mod`.
- FFmpeg and yt-dlp on `PATH`, or downloaded into `resources/<platform>/bin`.
- A supported yt-dlp JavaScript runtime such as Deno on `PATH` for extractor
  workflows that require one; it is not bundled by the current release.
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

The application can use system tools, but release packages are intended to include platform-specific yt-dlp and FFmpeg binaries. Run the helper with Bash:

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

Supported target names include `windows-amd64`, `windows-arm64`, `linux-amd64`, `linux-arm64`, `darwin-amd64`, and `darwin-arm64`. Windows/Linux FFmpeg archives are verified against the recorded SHA-256 values. macOS uses a Homebrew bottle on a runner matching the target architecture (`macos-13` for amd64 and `macos-latest` for arm64); Homebrew verifies that bottle before installation.

Optional environment variables:

```bash
YTDLP_TAG=2026.08.19 FFMPEG_RELEASE=latest \
  bash scripts/fetch-tools.sh linux-amd64
```

The default yt-dlp version and SHA-256 checksums are recorded in the script.
The FFmpeg rolling release is accepted only when its recorded checksum still
matches; update the release name and checksum together when intentionally
upgrading it. Do not commit the downloaded files: `resources/*/bin/*` is
ignored because FFmpeg binaries are large.

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

1. Packaged `resources/bin/<tool>`.
2. Packaged `resources/<tool>`.
3. Development `resources/<os>-<arch>/bin/<tool>`.
4. System `PATH`.

The platform directories are flattened/selected by MyGo during packaging. During `go run`, the platform subdirectory remains in the repository, which is why the development lookup exists.

## Application metadata

`mygo.json` currently defines:

- Application name: `Video Downloader`.
- Identifier: `com.fazigondal.videodownloader`.
- Version: `2.1.0`.
- Output directory: `build`.
- Linux maintainer metadata.

When publishing a release, the Git tag must match the version: `mygo.json` version `2.1.0` requires tag `v2.1.0`.

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

`.github/workflows/ci.yml` runs on pushes to `main`/`master` and pull requests. It uses the Go version from `go.mod`, tests the committed dependency graph, runs vet, and performs a Linux MyGo packaging smoke build.

### Release

`.github/workflows/release.yml` runs on `v*` tags. It:

1. Checks that the tag matches `mygo.json`.
2. Creates a draft GitHub Release.
3. Fetches verified helper tools for Windows/Linux and architecture-matched Homebrew FFmpeg for macOS.
4. Builds Windows and Linux amd64/arm64 plus macOS amd64/arm64 with `go tool mygo` and `CGO_ENABLED=0`.
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

Run the helper script using Bash, check `resources/<platform>/bin`, or install the tools on `PATH`. Restart the app after changing bundled resources.

### Linux executable does not start

Install the MyGo Linux runtime libraries for the distribution. Prefer the `.deb` package when available.
