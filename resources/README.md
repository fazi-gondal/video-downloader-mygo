# Bundled tools (yt-dlp + FFmpeg)

MyGo ships files under platform directories:

```
resources/
  windows-amd64/bin/yt-dlp.exe
  windows-amd64/bin/ffmpeg.exe
  windows-amd64/bin/ffprobe.exe
  linux-amd64/bin/…
  …
  icon.png          # shared (not platform-specific)
```

At runtime `internal/tools` resolves:

1. `App.Path(PathResources)/bin/<tool>`
2. system `PATH`

## Populate binaries

```bash
# current machine only
./scripts/fetch-tools.sh

# every platform (CI / release packaging)
./scripts/fetch-tools.sh all
```

Sources:
- **yt-dlp** — [yt-dlp releases](https://github.com/yt-dlp/yt-dlp/releases)
- **ffmpeg** — [yt-dlp/FFmpeg-Builds](https://github.com/yt-dlp/FFmpeg-Builds/releases) (GPL static)

Do **not** commit multi‑hundred‑MB binaries unless you accept repo size; the release workflow downloads them before `mygo build`.

`.gitignore` ignores `resources/*/bin/*` so only the script + icon stay in git.
