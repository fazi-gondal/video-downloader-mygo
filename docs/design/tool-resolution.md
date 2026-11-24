# Tool resolution and in-app setup

This document records the lookup order and why it is safe to install FFmpeg
from inside the app. It covers the code in `internal/tools/resolve.go`,
`internal/services/toolinstall.go`, and the Home setup card.

## Lookup order

`ffmpeg` and `ffprobe`:

1. **Explicit path from Settings → Tools.** The user's choice wins.
2. **App-managed directory.** `~/Library/Application Support/VideoDownloader/bin`
   on macOS (the platform config directory elsewhere). An install made from the
   setup card must be the version the app actually runs.
3. **Bundled resources.** Windows and Linux packages ship static binaries.
   macOS has no bundled FFmpeg.
4. **System installations.** Homebrew, MacPorts, and `PATH`.

Every candidate is executed with `-version` and cached. A present-but-broken
candidate is skipped, so a stale managed file cannot shadow a working system
copy. A probe that times out is cached briefly (30 seconds) so the UI cannot
spin on a hung launcher.

`yt-dlp` uses the same explicit-path rule, then chooses the highest version
among the app-managed, bundled, and system copies. Ties prefer the app-managed
copy, then the bundled copy, then the system copy. This preserves the previous
"newer version wins" behavior while letting an in-app install take effect when
it is actually newer. `youtube-dl` remains a last-resort system fallback.

The startup path uses an existence-only check so a slow first `yt-dlp --version`
does not delay the window; the asynchronous probe updates the services and the
Tools card as soon as each result arrives.

## Installing FFmpeg

The Home setup card appears when a required tool is missing. The user chooses
one of:

- **Install with Homebrew** when `/opt/homebrew/bin/brew` or
  `/usr/local/bin/brew` exists.
- **Download FFmpeg** when Homebrew is absent and the platform has a pinned
  static source.

The static source is pinned to `b6.1.1` with an app-embedded SHA-256 for each
architecture. The checksum is not read from the download response. The files
are downloaded to a temporary directory inside the managed directory, verified,
executed with `-version`, and only then moved into place. Existing files are
backed up first; any failure or cancellation removes the temporary files and
restores the backups. Download progress is shown, and the download can be
cancelled.

After a successful install the probe cache is cleared. The partial result for
`ffmpeg` updates the service paths immediately; the rest of the probe continues
in the background. No app restart is needed.

The setup card is dismissed only for the current session. A blocked download
or conversion opens the same card again. No install runs without a click, and
nothing writes inside the `.app` bundle.

## Why managed tools win over system tools for FFmpeg

The setup card exists so a user can install a working FFmpeg without a
terminal. If system copies were preferred over the managed copy, pressing
"Install" could finish successfully while the app kept using an older or
broken system binary. The managed binary is therefore preferred for FFmpeg and
FFprobe; the Settings card still shows the effective path and source, and an
explicit path can override it.

`yt-dlp` is different because extractor fixes are version-sensitive: a newer
system copy should still win. The app-managed copy only wins when it is newer
(or equal, as a stability tie-break).
