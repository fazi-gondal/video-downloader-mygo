# Changelog

All notable user-visible changes are recorded here. Release tags use the version in `mygo.json` with a `v` prefix.

## [Unreleased]

## [3.0.0] - 2026-10-21

### Added

- Native MyGo desktop screens for Home, Downloads, Converter, History, Settings, and About with refreshed navigation, thumbnails, status pills, empty states, and responsive layouts.
- ARY Plus episode and series support, including canonical series and share URLs, platform redirects, HLS quality selection, thumbnails, duration metadata, and DRM detection.
- Pause and resume for active downloads, preserving yt-dlp partial files and staging directories during the current application session.
- YouTube 360° handling that prefers equirectangular streams and excludes packed mesh formats from automatic and manual video choices.
- Central VP9 preference in Settings with automatic fallback when VP9 is unavailable.
- Creator-uploaded subtitle selection with custom language grids, SRT conversion, MKV embedding, and subtitle-only HTTP 429 warnings.
- Download progress metrics for bytes, approximate totals, speed, ETA, and post-processing state.
- FFmpeg-backed conversion queue with remux and container-aware re-encode support, including lossy audio bitrate choices from 128 kbps through 320 kbps.

### Changed

- Audio tracks are now grouped by language and reduced to one best-quality Opus track per language; low, medium, and high variants of the same language are no longer shown as separate tracks.
- The audio-track selector is shown only when analyzed media provides multiple languages.
- YouTube downloads do not request browser cookies by default; browser-cookie import remains an explicit Network setting.
- Multiple audio tracks or embedded subtitles automatically require MKV, while format and size estimates account for physical payloads only once.
- Release packaging uses the pinned MyGo tool and verified platform-specific yt-dlp and FFmpeg resources.

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
