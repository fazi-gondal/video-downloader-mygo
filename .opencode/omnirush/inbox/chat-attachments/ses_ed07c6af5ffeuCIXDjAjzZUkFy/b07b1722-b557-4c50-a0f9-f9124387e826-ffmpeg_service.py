"""FFmpeg binary resolution and direct media conversion.

Resolution order: app-bundled full toolchain, system PATH, cached
``static-ffmpeg`` toolchain, then the binary bundled with the ``imageio-ffmpeg``
package. Note that imageio-ffmpeg ships ffmpeg but NOT ffprobe, so probing
degrades to extension/codec heuristics when only that fallback is available.
"""

from __future__ import annotations

import json
import logging
import shutil
import subprocess
import sys
import threading
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Literal

from video_downloader.config.constants import BUNDLED_FFMPEG_NAME, BUNDLED_FFPROBE_NAME
from video_downloader.core.errors import ConversionError, DownloadCancelled, FFmpegNotFoundError
from video_downloader.models.conversion import ConversionMode
from video_downloader.utils.bundled_tools import find_bundled_tool

logger = logging.getLogger(__name__)

ProgressCallback = Callable[[float | None], None]  # percent 0..1 or None (unknown)

# Codecs each container can hold without re-encoding (conservative table)
_CONTAINER_VCODECS: dict[str, frozenset[str]] = {
    "mp4": frozenset({"h264", "hevc", "av1", "mpeg4"}),
    "mkv": frozenset({"h264", "hevc", "av1", "vp8", "vp9", "mpeg4", "mpeg2video", "theora"}),
    "webm": frozenset({"vp8", "vp9", "av1"}),
    "avi": frozenset({"mpeg4", "msmpeg4v3", "mjpeg"}),
}
_CONTAINER_ACODECS: dict[str, frozenset[str]] = {
    "mp4": frozenset({"aac", "mp3", "alac", "ac3", "opus"}),
    "mkv": frozenset({"aac", "mp3", "opus", "vorbis", "flac", "ac3", "eac3", "dts"}),
    "webm": frozenset({"opus", "vorbis"}),
    "avi": frozenset({"mp3", "ac3", "pcm_s16le"}),
}
# Fallback when ffprobe is unavailable: extensions whose typical codecs fit the target
_EXT_REMUX_TARGETS: dict[str, frozenset[str]] = {
    "mkv": frozenset({"mp4", "webm", "mkv"}),
    "mp4": frozenset({"mkv", "mp4"}),
    "webm": frozenset({"mkv", "webm"}),
    "m4a": frozenset({"mp4", "mkv"}),
}

_AUDIO_CODEC_ARGS: dict[str, list[str]] = {
    "mp3": ["-c:a", "libmp3lame"],
    "m4a": ["-c:a", "aac"],
    "aac": ["-c:a", "aac"],
    "flac": ["-c:a", "flac"],
    "opus": ["-c:a", "libopus"],
    "wav": ["-c:a", "pcm_s16le"],
}


@dataclass(frozen=True)
class FFmpegLocation:
    ffmpeg_path: Path | None
    ffprobe_path: Path | None
    source: Literal["path", "bundled_full", "bundled", "missing"]


def _static_ffmpeg_paths() -> tuple[Path, Path] | None:
    """Already-downloaded static-ffmpeg binaries (ffmpeg AND ffprobe), if any.

    Never triggers a download: fetching happens in ensure_full_toolchain().
    """
    try:
        import static_ffmpeg
    except ImportError:
        return None
    suffix = ".exe" if sys.platform.startswith("win") else ""
    bin_root = Path(static_ffmpeg.__file__).parent / "bin"
    for platform_dir in sorted(bin_root.glob("*/")):
        ffmpeg = platform_dir / f"ffmpeg{suffix}"
        ffprobe = platform_dir / f"ffprobe{suffix}"
        if ffmpeg.is_file() and ffprobe.is_file():
            return ffmpeg, ffprobe
    return None


class FFmpegService:
    def __init__(self) -> None:
        self._location: FFmpegLocation | None = None
        self._lock = threading.Lock()

    def resolve(self) -> FFmpegLocation:
        with self._lock:
            if self._location is not None:
                return self._location

            bundled_ffmpeg = find_bundled_tool(BUNDLED_FFMPEG_NAME)
            bundled_ffprobe = find_bundled_tool(BUNDLED_FFPROBE_NAME)
            if bundled_ffmpeg and bundled_ffprobe:
                self._location = FFmpegLocation(
                    bundled_ffmpeg, bundled_ffprobe, "bundled_full"
                )
                logger.info("Using app-bundled ffmpeg toolchain at %s", bundled_ffmpeg.parent)
                return self._location

            ffmpeg = shutil.which("ffmpeg")
            ffprobe = shutil.which("ffprobe")
            if ffmpeg:
                self._location = FFmpegLocation(
                    Path(ffmpeg), Path(ffprobe) if ffprobe else None, "path"
                )
                logger.info("Using system ffmpeg at %s", ffmpeg)
                return self._location

            if static := _static_ffmpeg_paths():
                self._location = FFmpegLocation(static[0], static[1], "bundled_full")
                logger.info("Using static-ffmpeg toolchain at %s", static[0].parent)
                return self._location

            try:
                import imageio_ffmpeg

                bundled = Path(imageio_ffmpeg.get_ffmpeg_exe())
                self._location = FFmpegLocation(bundled, None, "bundled")
                logger.info("Using bundled ffmpeg at %s (no ffprobe)", bundled)
            except Exception as exc:  # ImportError or download failure
                logger.warning("No ffmpeg available: %s", exc)
                self._location = FFmpegLocation(None, None, "missing")
            return self._location

    def invalidate(self) -> None:
        with self._lock:
            self._location = None

    def ensure_full_toolchain(self, on_ready: Callable[[], None] | None = None) -> None:
        """Fetch static ffmpeg+ffprobe in the background when ffprobe is missing.

        One-time download cached on disk; subsequent resolves pick it up.
        `on_ready` is invoked FROM THE WORKER THREAD after a successful
        download — bridge to the UI loop (e.g. publish on the EventBus)
        before touching any control.
        """
        if self.has_ffprobe:
            return

        def fetch() -> None:
            try:
                from static_ffmpeg import run

                run.get_or_fetch_platform_executables_else_raise()
            except Exception as exc:
                logger.warning("Could not fetch static ffmpeg toolchain: %s", exc)
            else:
                self.invalidate()
                logger.info("static-ffmpeg toolchain ready (ffprobe available)")
                if on_ready is not None:
                    on_ready()

        threading.Thread(target=fetch, name="ffmpeg-fetch", daemon=True).start()

    @property
    def is_available(self) -> bool:
        return self.resolve().ffmpeg_path is not None

    @property
    def has_ffprobe(self) -> bool:
        return self.resolve().ffprobe_path is not None

    def ytdlp_location_arg(self) -> str | None:
        """Value for ``ydl_opts['ffmpeg_location']``.

        The containing directory when ffmpeg and ffprobe live together (so
        yt-dlp finds both); otherwise the path to the ffmpeg binary itself.
        """
        location = self.resolve()
        if location.ffmpeg_path is None:
            return None
        if (
            location.ffprobe_path is not None
            and location.ffprobe_path.parent == location.ffmpeg_path.parent
        ):
            return str(location.ffmpeg_path.parent)
        return str(location.ffmpeg_path)

    # ------------------------------------------------------------------
    # Probing

    def probe(self, src: Path) -> dict | None:
        """Return ffprobe JSON for *src*, or None when ffprobe is missing."""
        location = self.resolve()
        if not location.ffprobe_path:
            return None
        try:
            result = subprocess.run(
                [
                    str(location.ffprobe_path),
                    "-v", "error",
                    "-print_format", "json",
                    "-show_format", "-show_streams",
                    str(src),
                ],
                capture_output=True,
                text=True,
                encoding="utf-8",
                errors="replace",
                timeout=30,
                check=True,
            )
            if not result.stdout:
                return None
            return json.loads(result.stdout)
        except Exception as exc:
            logger.warning("ffprobe failed for %s: %s", src, exc)
            return None

    def can_remux(self, src: Path, target_container: str) -> bool:
        """Whether *src* can be remuxed (lossless) into *target_container*."""
        target = target_container.lower()
        info = self.probe(src)
        if info is not None:
            vcodecs = _CONTAINER_VCODECS.get(target, frozenset())
            acodecs = _CONTAINER_ACODECS.get(target, frozenset())
            for stream in info.get("streams", []):
                codec = (stream.get("codec_name") or "").lower()
                kind = stream.get("codec_type")
                if kind == "video" and codec not in vcodecs:
                    return False
                if kind == "audio" and codec not in acodecs:
                    return False
            return True
        # Heuristic fallback without ffprobe
        ext = src.suffix.lstrip(".").lower()
        if ext == target:
            return True
        return target in _EXT_REMUX_TARGETS.get(ext, frozenset())

    def media_duration(self, src: Path) -> float | None:
        info = self.probe(src)
        if info is None:
            return None
        duration = info.get("format", {}).get("duration")
        try:
            return float(duration) if duration is not None else None
        except (TypeError, ValueError):
            return None

    # ------------------------------------------------------------------
    # Conversions

    def convert_video(
        self,
        src: Path,
        target_container: str,
        mode: ConversionMode,
        on_progress: ProgressCallback | None = None,
        cancel_event: threading.Event | None = None,
    ) -> Path:
        dst = self._output_path(src, target_container)
        if mode is ConversionMode.REMUX:
            args = ["-c", "copy"]
        else:
            args = self._reencode_args(target_container)
        self._run_ffmpeg(src, dst, args, on_progress, cancel_event)
        return dst

    def convert_audio(
        self,
        src: Path,
        target_format: str,
        bitrate_kbps: int | None = None,
        on_progress: ProgressCallback | None = None,
        cancel_event: threading.Event | None = None,
    ) -> Path:
        codec_args = _AUDIO_CODEC_ARGS.get(target_format.lower())
        if codec_args is None:
            raise ConversionError(f"Unsupported audio format: {target_format}")
        args = ["-vn", *codec_args]
        if bitrate_kbps and target_format.lower() not in ("flac", "wav"):
            args += ["-b:a", f"{bitrate_kbps}k"]
        dst = self._output_path(src, target_format)
        self._run_ffmpeg(src, dst, args, on_progress, cancel_event)
        return dst

    # ------------------------------------------------------------------

    @staticmethod
    def _reencode_args(target_container: str) -> list[str]:
        target = target_container.lower()
        if target == "webm":
            return [
                "-c:v", "libvpx-vp9",
                "-crf", "32",
                "-b:v", "0",
                "-row-mt", "1",
                "-threads", "0",
                "-speed", "4",
                "-c:a", "libopus",
            ]
        if target == "avi":
            return ["-c:v", "mpeg4", "-q:v", "4", "-c:a", "libmp3lame", "-q:a", "3"]
        # mp4 / mkv default: H.264 + AAC
        return [
            "-c:v", "libx264",
            "-crf", "22",
            "-preset", "faster",
            "-threads", "0",
            "-c:a", "aac",
            "-b:a", "192k",
        ]

    @staticmethod
    def _output_path(src: Path, target_ext: str) -> Path:
        from video_downloader.utils.paths import unique_path

        return unique_path(src.with_suffix(f".{target_ext.lstrip('.').lower()}"))

    def _run_ffmpeg(
        self,
        src: Path,
        dst: Path,
        codec_args: list[str],
        on_progress: ProgressCallback | None,
        cancel_event: threading.Event | None,
    ) -> None:
        location = self.resolve()
        if not location.ffmpeg_path:
            raise FFmpegNotFoundError("ffmpeg is not available")

        duration = self.media_duration(src)
        cmd = [
            str(location.ffmpeg_path),
            "-y", "-nostdin", "-hide_banner", "-nostats",
            "-i", str(src),
            # Map video and audio streams, ignoring incompatible attachments/data
            "-map", "0:v?",
            "-map", "0:a?",
            *codec_args,
            "-progress", "pipe:1",
            str(dst),
        ]
        logger.info("Running: %s", " ".join(cmd))
        process = subprocess.Popen(
            cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            errors="replace",
        )

        stderr_lines: list[str] = []

        def _drain_stderr() -> None:
            if process.stderr:
                for err_line in process.stderr:
                    stderr_lines.append(err_line)

        stderr_thread = threading.Thread(target=_drain_stderr, daemon=True)
        stderr_thread.start()

        try:
            assert process.stdout is not None
            for line in process.stdout:
                if cancel_event is not None and cancel_event.is_set():
                    process.kill()
                    process.wait()
                    dst.unlink(missing_ok=True)
                    raise DownloadCancelled()
                line = line.strip()
                if line.startswith("out_time_us=") and on_progress:
                    try:
                        out_seconds = int(line.split("=", 1)[1]) / 1_000_000
                    except ValueError:
                        continue
                    percent = min(1.0, out_seconds / duration) if duration else None
                    on_progress(percent)
            returncode = process.wait()
        except Exception:
            if process.poll() is None:
                process.kill()
                process.wait()
            raise
        finally:
            stderr_thread.join(timeout=1.0)

        if returncode != 0:
            stderr = "".join(stderr_lines)
            dst.unlink(missing_ok=True)
            logger.error("ffmpeg failed (%s): %s", returncode, stderr[-2000:])
            raise ConversionError(f"ffmpeg exited with code {returncode}", detail=stderr[-2000:])
        if on_progress:
            on_progress(1.0)

    def merge_audio_tracks(
        self,
        base_file: Path,
        extra_audio: list[tuple[Path, str]],  # (file, language_tag)
        output: Path,
        first_lang: str = "",
        cancel_event: threading.Event | None = None,
    ) -> None:
        """Merge *base_file* (video + first audio) with *extra_audio* tracks into *output* MKV.

        Uses FFmpeg copy-mux so there is no re-encoding.  Each audio track
        is tagged with its BCP-47 language code so media players can label them.

        Args:
            base_file:   Path to the MKV that already contains the video and the
                         first (primary) audio stream.
            extra_audio: List of ``(audio_file, language_tag)`` pairs.  Each
                         audio_file is a single-stream file (e.g. .webm opus).
            output:      Final output path (always .mkv).
            first_lang:  Language tag for the base audio track (audio stream 0).
            cancel_event: Optional threading event; kill ffmpeg if set.
        """
        location = self.resolve()
        if not location.ffmpeg_path:
            raise FFmpegNotFoundError("ffmpeg is not available")

        cmd = [str(location.ffmpeg_path), "-y", "-hide_banner", "-nostats"]

        # Input files
        cmd += ["-i", str(base_file)]
        for audio_path, _ in extra_audio:
            cmd += ["-i", str(audio_path)]

        # Map video, first audio, and subtitles from base file, then each extra audio input
        cmd += ["-map", "0:v"]
        cmd += ["-map", "0:a:0"]
        cmd += ["-map", "0:s?"]
        for idx in range(1, len(extra_audio) + 1):
            cmd += ["-map", f"{idx}:a:0"]

        # Copy video and subtitles losslessly; encode audio to 192k AAC for universal playback
        cmd += ["-c:v", "copy"]
        cmd += ["-c:s", "copy"]
        cmd += ["-c:a", "aac", "-b:a", "192k"]

        # Ensure only the first audio stream has default disposition so players switch cleanly
        cmd += ["-disposition:a", "0"]
        cmd += ["-disposition:a:0", "default"]

        lang_titles = {
            "en": "English", "de": "German", "fr": "French", "es": "Spanish",
            "it": "Italian", "pt": "Portuguese", "ru": "Russian", "ja": "Japanese",
            "ko": "Korean", "zh": "Chinese", "zh-Hans": "Chinese (Simplified)",
            "zh-Hant": "Chinese (Traditional)", "hi": "Hindi", "ar": "Arabic",
            "bn": "Bangla", "tr": "Turkish", "pl": "Polish", "vi": "Vietnamese",
            "id": "Indonesian", "th": "Thai", "ta": "Tamil", "te": "Telugu",
            "ml": "Malayalam", "ur": "Urdu", "nl": "Dutch",
        }

        # Tag each audio stream with its language code and title.
        if first_lang:
            cmd += ["-metadata:s:a:0", f"language={first_lang}"]
            if title := lang_titles.get(first_lang):
                cmd += ["-metadata:s:a:0", f"title={title}"]
        for i, (_, lang_tag) in enumerate(extra_audio):
            audio_stream_idx = 1 + i
            if lang_tag:
                cmd += [f"-metadata:s:a:{audio_stream_idx}", f"language={lang_tag}"]
                if title := lang_titles.get(lang_tag):
                    cmd += [f"-metadata:s:a:{audio_stream_idx}", f"title={title}"]

        cmd += [str(output)]

        logger.info("Merging %d audio tracks: %s", len(extra_audio) + 1, " ".join(cmd))
        process = subprocess.Popen(
            cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            errors="replace",
        )
        try:
            # communicate() drains stdout and stderr concurrently, avoiding OS pipe deadlocks
            _, stderr = process.communicate()
        except Exception:
            if process.poll() is None:
                process.kill()
                process.wait()
            raise

        if process.returncode != 0:
            output.unlink(missing_ok=True)
            err_msg = (stderr or "")[-2000:]
            logger.error("ffmpeg merge failed (%s): %s", process.returncode, err_msg)
            raise ConversionError(
                f"ffmpeg merge exited with code {process.returncode}", detail=err_msg
            )
        logger.info("Merged output: %s", output)
