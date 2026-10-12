"""Pure functions translating UI choices into yt-dlp options.

No I/O here: everything is unit-testable without network or yt-dlp itself.
"""

from __future__ import annotations

import logging
import re
from pathlib import Path
from typing import Any

from video_downloader.config.constants import (
    LOSSLESS_AUDIO_FORMATS,
    OUTPUT_TEMPLATE,
    PLAYLIST_OUTPUT_TEMPLATE,
)
from video_downloader.config.settings import AppSettings
from video_downloader.models.download import DownloadMode, DownloadRequest

# BCP-47 language code pattern (e.g. 'en', 'de', 'zh-Hant', 'pt-BR')
_LANG_CODE_RE = re.compile(r"^[a-z]{2,3}(-[A-Za-z]{2,4})?$")
VP9_FORMAT_SORT = ["vcodec:vp9", "res", "fps", "acodec:opus", "acodec:m4a"]
FAST_YOUTUBE_CLIENTS = ["web_embedded", "default"]
RICH_YOUTUBE_CLIENTS = ["all"]


def _audio_selector(track_key: str) -> str:
    """Return the yt-dlp format filter for one audio track selector key.

    Language codes (e.g. ``'en'``, ``'zh-Hant'``) are converted to the stable
    ``ba[language=XX]`` filter.  Raw format IDs (e.g. ``'251-0'``, ``'140'``)
    are passed through unchanged.
    """
    if _LANG_CODE_RE.match(track_key):
        return f"ba[language={track_key}]"
    return track_key


def build_format_selector(req: DownloadRequest) -> str:
    """Build the yt-dlp ``format`` selector string for *req*."""
    # Explicit format ids picked from the format explorer win over presets
    if req.mode is DownloadMode.VIDEO_AUDIO and req.video_format_id and req.audio_format_id:
        return f"{req.video_format_id}+{req.audio_format_id}"
    if req.mode is DownloadMode.VIDEO_ONLY and req.video_format_id:
        return req.video_format_id
    if req.mode is DownloadMode.AUDIO_ONLY and req.audio_format_id:
        return req.audio_format_id

    if req.mode is DownloadMode.AUDIO_ONLY:
        return "ba/b"

    filters = ""
    if req.max_height:
        filters += f"[height<=?{req.max_height}]"
    if req.max_fps:
        filters += f"[fps<=?{req.max_fps}]"

    if req.mode is DownloadMode.VIDEO_ONLY:
        if filters:
            return f"bv{filters}/bv/b{filters}/b"
        return "bv/b"

    # Specific multiple audio tracks selected by the user
    if req.selected_audio_track_ids and req.mode is DownloadMode.VIDEO_AUDIO:
        # Each key is either a BCP-47 language code ("de", "zh-Hant") or a
        # raw format ID.  _audio_selector() converts language codes to the
        # ba[language=XX] filter which is stable across yt-dlp sessions.
        audio_parts = [_audio_selector(k) for k in req.selected_audio_track_ids]
        audio_part = "+".join(audio_parts)
        video_part = req.video_format_id or (f"bv*{filters}" if filters else "bv*")

        if len(req.selected_audio_track_ids) == 1:
            # Single track: try language/ID filter, fall back to best audio
            return f"{video_part}+{audio_part}/{video_part}+ba/b"

        # Multiple tracks: try exact language selection first.
        # If some languages are unavailable (PO token / JS challenge failure),
        # fall back to mergeall which downloads ALL available audio streams.
        # Final fallback: best single audio so the download never fails entirely.
        return (
            f"{video_part}+{audio_part}"
            f"/{video_part}+mergeall[vcodec=none]"
            f"/{video_part}+ba/b"
        )

    # VIDEO_AUDIO presets: best video matching filters + best audio, with fallbacks
    if req.multi_audio:
        if filters:
            return f"bv*{filters}+mergeall[vcodec=none]/b{filters}/b"
        return "bv*+mergeall[vcodec=none]/b"

    if filters:
        return f"bv*{filters}+ba/b{filters}/b"
    return "bv*+ba/b"


def build_postprocessors(
    req: DownloadRequest, have_ffprobe: bool = True
) -> list[dict[str, Any]]:
    """Postprocessor chain; converters first, then metadata/thumbnail embedding."""
    postprocessors: list[dict[str, Any]] = []

    # When multiple audio tracks are merged, MKV is the effective container
    # regardless of what the user chose (only MKV reliably stores multi-audio).
    is_multi_audio = req.multi_audio or len(req.selected_audio_track_ids) > 1
    prefer_vp9 = req.prefer_vp9_video and req.mode is not DownloadMode.AUDIO_ONLY
    effective_container = (
        "mkv"
        if ((is_multi_audio or prefer_vp9) and req.mode is not DownloadMode.AUDIO_ONLY)
        else req.container
    )

    if req.mode is DownloadMode.AUDIO_ONLY:
        pp: dict[str, Any] = {
            "key": "FFmpegExtractAudio",
            "preferredcodec": req.audio_format,
        }
        if req.audio_bitrate_kbps and req.audio_format not in LOSSLESS_AUDIO_FORMATS:
            pp["preferredquality"] = str(req.audio_bitrate_kbps)
        postprocessors.append(pp)
    elif effective_container:
        # Remux into the effective container when streams allow it losslessly
        postprocessors.append(
            {"key": "FFmpegVideoRemuxer", "preferedformat": effective_container}
        )

    if req.write_subtitles and req.mode is not DownloadMode.AUDIO_ONLY:
        postprocessors.append(
            {"key": "FFmpegEmbedSubtitle", "already_have_subtitle": False}
        )

    if req.embed_metadata:
        postprocessors.append({"key": "FFmpegMetadata", "add_metadata": True})
    if req.embed_thumbnail and _thumbnail_embeddable(req, have_ffprobe, effective_container):
        postprocessors.append({"key": "EmbedThumbnail", "already_have_thumbnail": False})

    return postprocessors


def _thumbnail_embeddable(
    req: DownloadRequest, have_ffprobe: bool, effective_container: str | None = None
) -> bool:
    """Whether EmbedThumbnail can run for this request.

    Matroska embedding calls ffprobe fatally; without it, skip the
    postprocessor instead of failing the whole download.
    """
    if have_ffprobe:
        return True
    target = (
        req.audio_format if req.mode is DownloadMode.AUDIO_ONLY
        else (effective_container or req.container)
    ).lower()
    if target in ("mkv", "mka", "webm"):
        logging.getLogger(__name__).warning(
            "Skipping thumbnail embedding for %s: ffprobe is not available", target
        )
        return False
    return True


def build_output_template(req: DownloadRequest) -> str:
    if req.playlist_title:
        return PLAYLIST_OUTPUT_TEMPLATE
    return OUTPUT_TEMPLATE


def build_ydl_opts(
    req: DownloadRequest,
    settings: AppSettings,
    ffmpeg_location: str | None,
    logger: logging.Logger | None = None,
    have_ffprobe: bool = True,
) -> dict[str, Any]:
    """Full ``ydl_opts`` for one download (no hooks; the service adds those)."""
    output_dir = Path(req.output_dir).expanduser()
    needs_rich_youtube_clients = bool(req.multi_audio or req.selected_audio_track_ids)
    player_clients = (
        RICH_YOUTUBE_CLIENTS if needs_rich_youtube_clients else FAST_YOUTUBE_CLIENTS
    )
    opts: dict[str, Any] = {
        "format": build_format_selector(req),
        "outtmpl": str(output_dir / build_output_template(req)),
        "postprocessors": build_postprocessors(req, have_ffprobe=have_ffprobe),
        "noplaylist": True,
        "continuedl": True,
        "retries": 5,
        "fragment_retries": 5,
        "quiet": True,
        "noprogress": True,
        "no_color": True,
        "windowsfilenames": False,
        "restrictfilenames": False,
        # YouTube JS challenges: solver scripts come from the bundled
        # yt-dlp-ejs package; GitHub stays allowed as a version-drift fallback.
        "remote_components": ["ejs:github"],
        "js_runtimes": _get_js_runtimes(),
        # Use all player clients so YouTube exposes every language dub track
        # (e.g. MrBeast has 22 language audio tracks — only visible with client=all)
        "extractor_args": {"youtube": {"player_client": player_clients}},
    }
    concurrent_fragments = max(1, int(settings.concurrent_fragments or 1))
    if concurrent_fragments > 1:
        opts["concurrent_fragment_downloads"] = concurrent_fragments

    if logger is not None:
        opts["logger"] = logger

    if ffmpeg_location:
        opts["ffmpeg_location"] = ffmpeg_location

    if req.mode is not DownloadMode.AUDIO_ONLY and req.container:
        # When multiple audio tracks are selected, only MKV can hold them all.
        # MP4 / WebM silently drop extra audio streams during FFmpeg merge.
        is_multi_audio = (
            req.multi_audio
            or len(req.selected_audio_track_ids) > 1
        )
        prefer_vp9 = req.prefer_vp9_video
        effective_container = "mkv" if (is_multi_audio or prefer_vp9) else req.container
        opts["merge_output_format"] = effective_container
        if prefer_vp9 and not req.video_format_id:
            opts["format_sort"] = list(VP9_FORMAT_SORT)
        # Only apply codec-preference sort when NOT using explicit track IDs
        # (explicit IDs are already exact; format_sort can fight them).
        elif (
            not req.video_format_id
            and not req.selected_audio_track_ids
            and not req.multi_audio
        ):
            if req.container == "mp4":
                opts["format_sort"] = ["vcodec:h264", "acodec:m4a"]
            elif req.container == "webm":
                opts["format_sort"] = ["vcodec:vp9", "acodec:opus"]

    if req.embed_thumbnail:
        opts["writethumbnail"] = True

    if req.write_subtitles:
        opts["writesubtitles"] = True
        opts["writeautomaticsub"] = True
        if not req.subtitle_langs or "all" in req.subtitle_langs:
            opts["subtitleslangs"] = ["all"]
        else:
            opts["subtitleslangs"] = list(req.subtitle_langs)

    # audio_multistreams must be set whenever more than one audio track is
    # involved — otherwise FFmpeg only keeps the first stream it encounters.
    if (
        req.mode is not DownloadMode.AUDIO_ONLY
        and (req.multi_audio or len(req.selected_audio_track_ids) > 1)
    ):
        opts["audio_multistreams"] = True

    if settings.proxy:
        opts["proxy"] = settings.proxy
    if settings.cookies_browser:
        opts["cookiesfrombrowser"] = (settings.cookies_browser,)
    if settings.custom_headers:
        opts["http_headers"] = dict(settings.custom_headers)
    if settings.rate_limit_kbps:
        opts["ratelimit"] = settings.rate_limit_kbps * 1024

    return opts


def _get_js_runtimes() -> dict[str, dict[str, Any]]:
    from video_downloader.utils.env import find_executable

    runtimes: dict[str, dict[str, Any]] = {"deno": {}, "node": {}, "bun": {}}
    for name in runtimes:
        if executable := find_executable(name):
            runtimes[name] = {"path": str(executable)}
    return runtimes


def build_analysis_opts(settings: AppSettings, *, rich: bool = False) -> dict[str, Any]:
    """Options for fast URL analysis (flat playlist extraction)."""
    player_clients = RICH_YOUTUBE_CLIENTS if rich else FAST_YOUTUBE_CLIENTS
    opts: dict[str, Any] = {
        "extract_flat": "in_playlist",
        "skip_download": True,
        "quiet": True,
        "noprogress": True,
        "no_color": True,
        "socket_timeout": 30,
        "retries": 5,
        "extractor_retries": 3,
        "remote_components": ["ejs:github"],
        "js_runtimes": _get_js_runtimes(),
        # Keep first-pass analysis light; rich detail fetch opts into all clients.
        "extractor_args": {"youtube": {"player_client": player_clients}},
    }
    if settings.proxy:
        opts["proxy"] = settings.proxy
    if settings.cookies_browser:
        opts["cookiesfrombrowser"] = (settings.cookies_browser,)
    if settings.custom_headers:
        opts["http_headers"] = dict(settings.custom_headers)
    return opts
