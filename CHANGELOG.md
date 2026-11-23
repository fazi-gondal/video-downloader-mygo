# Changelog

All notable user-visible changes are recorded here. Release tags use the version in `mygo.json` with a `v` prefix.

## [Unreleased]

### Added

- **Page extractor**: recover every video from pages yt-dlp reports as
  unsupported (including players whose stream URL is JSON-escaped in an inline
  config), inline on Home. Select any subset, copy links, continue to the
  download options, or queue everything with best-quality defaults.
- **Native i18n** through MyGo's catalog (`en`, `zh-Hans`) with a Settings
  language card (follow system / 简体中文 / English) that applies and persists
  immediately. zh-Hant falls back to English.
- **Tools status** in Settings: yt-dlp, FFmpeg, and FFprobe versions, paths, and
  health with a re-check that runs off the render thread.
- Friendly, localized download errors; failure badges; empty-state actions;
  keyboard navigation shortcuts.

### Changed

- Home hero rebuilt on one center axis with a fixed spacing scale; the
  extractor expands in place with one synchronized height/offset animation
  instead of switching pages.
- Removed duplicate page headings; the extractor sidebar item is gone; the
  theme toggle moved to the top bar as a sun/moon button.
- History entries created by the extractor store their source page and
  re-resolve it on retry, so expired direct links no longer fail.
- macOS packaging uses pinned static FFmpeg/FFprobe builds; the fetch script
  rejects Homebrew-linked binaries, and bundled tools are probed at runtime.

## [3.3.0] - 2026-11-20

### Added

- **Minimize to tray**: a new "Minimize to tray" option in the close dialog lets
  downloads continue running in the background. A system-tray icon appears with
  "Show Video Downloader" and "Quit" menu items; clicking the icon restores the
  window instantly.

### Changed

- Redesigned format-selection (config) screen: mode icon cards, **ChipGroup-style
  card selectors** for container (mp4/mkv/webm), video quality (Best → 360p), and
  audio format (mp3/m4a/opus/flac/wav); bento two-column layout; fixed bottom
  action bar with live summary chips and size pill.
- Redesigned About screen: centered card, large accent logo block, version pill,
  outlined link buttons with icons, tech stack chips, license link.
- Redesigned Settings screen: glass card sections with icons, improved slider rows,
  section dividers, and tonal surface styling.
- Sidebar: theme-switching button moved to the bottom of the sidebar, styled
  identically to native SidebarItem entries (same padding, gap, icon, hover bg).
- History actions (open folder, re-download, delete) redesigned with icons
  matching the reference app.
- Close dialog updated from 3 → 4 buttons; "Minimize to tray" is the default
  suggestion when downloads are active.
- `OnWindowAllClosed` now guards against shutting down the app while a tray icon
  is active, keeping the event loop alive for background downloads.

### Fixed

- Use `!strings.EqualFold` instead of `strings.ToLower(x) != strings.ToLower(y)`
  in audio-track and subtitle sort helpers (SA6005).
- YouTube playlist downloads now use the playlist name as the output folder.

## [3.2.0] - 2026-10-31

### Added

- Closing with unfinished downloads offers to keep downloading, pause and close,
  or cancel downloads and close.
- Paused downloads persist their request, progress, and partial-file staging
  paths and are restored in the Downloads list after restarting the app.

### Changed

- History retries preserve the original selected video quality and format
  settings when those values are available.

## [3.1.1] - 2026-10-28

### Added

- Glass material styling across major application surfaces, rendered by MyGo on
  Windows, macOS, and Linux.
- Discrete Settings sliders for concurrent downloads (1–16) and concurrent
  fragments (1–32), with live values and settings import/save synchronization.

### Changed

- Pressing Enter in the Home URL field now starts media analysis.
- User-provided URLs passed to yt-dlp must be HTTP(S) URLs with a host; option-like
  inputs are rejected.

### Fixed

- Duration probing now has a five-second timeout so a stalled FFprobe process
  cannot block conversion indefinitely.

## [3.1.0] - 2026-10-28

### Fixed

- ARY Plus downloads use the built-in public client key when no environment override is set.
- Pasting a link and pressing Enter now starts analysis from the URL field.
- Settings files that omit fields now retain their defaults, including keeping source files after conversion unless the user explicitly disables that option.
- Playlist completion tracking works for untitled playlists and does not count late task events from earlier batches.
- Concurrent downloads with the same output name retain distinct files, and output-path lookup errors are reported instead of reusing an existing path.

## [3.0.0] - 2026-10-27

### Added

- Direct TikTok, Instagram, X/Twitter, and YouTube Shorts downloads: analyze a link to see metadata, then use the single **Download HD** action without entering the normal quality-selection page.
- Native MyGo desktop screens for Home, Downloads, Converter, History, Settings, and About with refreshed navigation, embedded SVG icons, thumbnails, status pills, empty states, and responsive layouts.
- ARY Plus episode and series support, including canonical series and share URLs, platform redirects, HLS quality selection, thumbnails, duration metadata, and DRM detection.
- Pause and resume for active downloads, preserving yt-dlp partial files and staging directories during the current application session.
- YouTube 360° handling that prefers equirectangular streams and excludes packed mesh formats from automatic and manual video choices.
- Central VP9 preference in Settings with automatic fallback when VP9 is unavailable.
- Creator-uploaded subtitle selection with custom language grids, SRT conversion, MKV embedding, and subtitle-only HTTP 429 warnings.
- Download progress metrics for bytes, approximate totals, speed, ETA, and post-processing state.
- FFmpeg-backed conversion queue with remux and container-aware re-encode support, including lossy audio bitrate choices from 128 kbps through 320 kbps.

### Changed

- YouTube playlist downloads now download into a folder named after the playlist title.
- Multi-track audio and subtitle selection prioritizes original audio first (if selected) and English subtitles first (if selected), falling back to alphabetical sorting.
- Windows background execution hides CMD console windows during video downloads, format conversions, and metadata probing.
- Social-video downloads use the best available video/audio source, MP4 output, single-video mode, and a cookie-free request. Watermarks already embedded in source pixels are not removed.
- Final filenames use the media title without an appended yt-dlp video ID; playlist entries retain their playlist index.
- Audio tracks are now grouped by language and reduced to one best-quality Opus track per language; low, medium, and high variants of the same language are no longer shown as separate tracks.
- The audio-track selector is shown only when analyzed media provides multiple languages.
- YouTube downloads do not request browser cookies by default; browser-cookie import remains an explicit Network setting.
- Multiple audio tracks or embedded subtitles automatically require MKV, while format and size estimates account for physical payloads only once.
- Release packaging uses MyGo v0.7.4 and verified platform-specific yt-dlp and FFmpeg resources.

### Fixed

- Prevented duplicate audio choices caused by technical quality or format-note variants.
- Preserved explicit one-sided video or audio format selections when building yt-dlp selectors.
- Prevented muxed fallback audio from being counted twice in size estimates.
- Improved cancellation cleanup, conversion finalization, background task updates, and bounded thumbnail/history retention.
- Improved validation and defensive copying for settings, download state, and conversion state.

## [2.2.1] - 2026-10-19

### Changed

- Direct stream playlist downloads (such as ARY Plus series episodes) now name files directly with the media title rather than prepending the playlist track index (e.g. `Dar-E-Nijaat - Episode 21.mp4`).

### Fixed

- Quality selection for pre-muxed HLS streams (ARY Plus) now accurately respects resolution caps (480p, 720p, 1080p, etc.) and explicit format IDs instead of falling back to 4K.
- Progress parsing now properly handles decimal values in estimated total sizes and speed/ETA emitted by yt-dlp, enabling accurate live percentage, video size, speed, and time remaining in the UI.

## [2.2.0] - 2026-10-18

### Added

- Native MyGo home, downloads, converter, history, settings, and about screen refinements.
- Sidebar icons, media thumbnails, status pills, empty states, history filters, and converter queue layout.
- Responsive three-column audio and subtitle selectors with bounded scrolling and label truncation.
- Automatic MKV presentation for multiple audio tracks or embedded subtitles.
- Estimated final download size based on stream metadata and duration.
- Download progress metrics for bytes, approximate totals, speed, and ETA.
- Per-track approximate size contributions for custom multi-audio selection.
- Audio conversion bitrate choices from 128 kbps through 320 kbps for lossy formats.
- Native ARY Plus (aryplus.tv) extractor supporting episode downloads up to 4K, series playlists, thumbnails, durations, and DRM protection detection.
- Windows/Linux/macOS CI packaging checks and pinned MyGo CLI usage.

### Changed

- Release packaging uses `go tool mygo` from the pinned module tool instead of an unpinned global CLI.
- Runtime helper resolution supports both packaged flattened resources and development platform directories.
- Linux release uploads include the generated `install.sh` artifact.
- Size estimates now sum physical video/audio payloads once, prefer selections that match the download container, and mark final-size uncertainty with `~`.
- Download progress parsing uses an explicit stdout marker and preserves a processing state while yt-dlp merges or post-processes streams.
- Unknown size, speed, and ETA values are shown explicitly while yt-dlp initializes instead of being hidden.

### Fixed

- Subtitle HTTP 429 responses no longer fail a valid media download when only subtitle retrieval fails.
- Multiple subtitle embedding uses SRT conversion and MKV remuxing for player compatibility.
- Audio/subtitle selector cells no longer expand outside the right side of the window.
- Muxed video+audio formats are no longer counted as video plus a second copy of the same audio stream.
- Custom multi-audio estimates no longer include unchecked tracks.
- One-sided explicit video or audio format selections are preserved in the yt-dlp selector.
- Audio-track bitrate parsing falls back to total bitrate when `abr` is unavailable.

## [2.0.0]

- Initial native MyGo application structure and core downloader/converter services.
- URL analysis, playlists, format selection, download queue, conversion queue, settings, and JSON history.
