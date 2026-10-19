# Changelog

All notable user-visible changes are recorded here. Release tags use the version in `mygo.json` with a `v` prefix.

## [Unreleased]

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
