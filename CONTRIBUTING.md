# Contributing

Thank you for contributing to Video Downloader for MyGo. The project is a native Go desktop application. Keep changes focused, preserve the native MyGo stack, and update documentation when user-visible behavior changes.

## Before you start

Install:

- Go 1.27.2.
- Git.
- FFmpeg and yt-dlp for end-to-end local downloads, or use the bundled-tool helper.
- Git Bash or another Bash environment for `scripts/fetch-tools.sh`.

Clone and validate the repository:

```bash
cd video-downloader-mygo
```

## Development workflow

Run the app with:

```bash
```

Use MyGo live development mode when iterating on the native UI:

```bash
```

Use the existing models, services, and UI primitives before adding a dependency. Do not replace native mygo inputs with a web frontend without an explicit design decision.

## Making changes

1. Read the relevant existing code and project documentation first.
2. Define the user-visible behavior and boundary cases.
3. Make the smallest complete change.
4. Add or update tests for behavior changes.
5. Run `gofmt` on changed Go files.
6. Run the full verification commands before opening a pull request.
7. Update `README.md`, `BUILD.md`, `resources/README.md`, or this file when commands, settings, packaging, or workflows change.

For UI changes, preserve keyboard access, scrolling behavior, native controls, and narrow-window layout. For downloads, do not log cookies, signed URLs, proxy credentials, or private media URLs.

## Testing checklist

```bash
```

For packaging/resource changes, also run:

```bash
CGO_ENABLED=0 go tool mygo build -platform linux/amd64 -skip-build-command
CGO_ENABLED=0 go tool mygo build -platform windows/amd64 -skip-build-command
```

For helper-script changes, run:

```bash
bash -n scripts/fetch-tools.sh
```

Use `go test -race ./...` when a C compiler is available. A missing GCC toolchain is an environment limitation, not a reason to change production builds away from `CGO_ENABLED=0`.

## Commit and pull request style

Use a conventional commit prefix:

- `feat:` user-visible functionality.
- `fix:` bug fix.
- `docs:` documentation only.
- `test:` test-only changes.
- `refactor:` behavior-preserving code structure change.
- `build:` packaging or workflow change.
- `chore:` maintenance.

Pull requests should include:

- What changed and why.
- How the change was tested.
- Any platform-specific checks that were unavailable.
- Screenshots for substantial UI changes.
- Notes about resource, workflow, or release changes.

Do not commit generated binaries, local settings, credentials, cookies, or downloaded helper tools.
