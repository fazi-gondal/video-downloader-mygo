"""Post-download conversion queue (single worker, same event model)."""

from __future__ import annotations

import logging
import queue
import threading
import uuid
from dataclasses import dataclass, field
from pathlib import Path

from video_downloader.core.errors import AppError, DownloadCancelled
from video_downloader.core.event_bus import EventBus
from video_downloader.core.events import (
    ConversionFinished,
    ConversionProgress,
    ConversionQueued,
)
from video_downloader.models.conversion import ConversionRequest, MediaKind
from video_downloader.services.ffmpeg_service import FFmpegService

logger = logging.getLogger(__name__)


@dataclass
class ConversionJob:
    request: ConversionRequest
    id: str = field(default_factory=lambda: uuid.uuid4().hex)
    done: bool = False
    error: str | None = None  # user_message_key
    output_path: Path | None = None
    cancel_event: threading.Event = field(default_factory=threading.Event)


class ConversionService:
    def __init__(self, ffmpeg: FFmpegService, bus: EventBus) -> None:
        self._ffmpeg = ffmpeg
        self._bus = bus
        self._queue: queue.Queue[ConversionJob | None] = queue.Queue()
        self._jobs: dict[str, ConversionJob] = {}
        self._lock = threading.Lock()
        self._worker: threading.Thread | None = None

    def enqueue(self, request: ConversionRequest) -> ConversionJob:
        job = ConversionJob(request=request)
        with self._lock:
            self._jobs[job.id] = job
        self._ensure_worker()
        self._bus.publish(ConversionQueued(task_id=job.id))
        self._queue.put(job)
        return job

    def cancel(self, job_id: str) -> None:
        job = self.get(job_id)
        if job and not job.done:
            job.cancel_event.set()

    def get(self, job_id: str) -> ConversionJob | None:
        with self._lock:
            return self._jobs.get(job_id)

    # ------------------------------------------------------------------

    def _ensure_worker(self) -> None:
        if self._worker is None or not self._worker.is_alive():
            self._worker = threading.Thread(
                target=self._worker_loop, name="conversion-worker", daemon=True
            )
            self._worker.start()

    def _worker_loop(self) -> None:
        while True:
            job = self._queue.get()
            if job is None:
                return
            if job.cancel_event.is_set():
                job.done = True
                self._bus.publish(
                    ConversionFinished(task_id=job.id, output_path=None, error_key=None)
                )
                continue
            self._run_job(job)

    def _run_job(self, job: ConversionJob) -> None:
        request = job.request

        def on_progress(percent: float | None) -> None:
            self._bus.publish(ConversionProgress(task_id=job.id, percent=percent))

        try:
            if request.kind is MediaKind.AUDIO:
                output = self._ffmpeg.convert_audio(
                    request.source,
                    request.target_format,
                    request.audio_bitrate_kbps,
                    on_progress,
                    job.cancel_event,
                )
            else:
                output = self._ffmpeg.convert_video(
                    request.source,
                    request.target_format,
                    request.mode,
                    on_progress,
                    job.cancel_event,
                )
            if not request.keep_original:
                request.source.unlink(missing_ok=True)
        except DownloadCancelled:
            job.done = True
            self._bus.publish(
                ConversionFinished(task_id=job.id, output_path=None, error_key=None)
            )
            logger.info("Conversion %s cancelled", job.id)
        except AppError as exc:
            job.done = True
            job.error = exc.user_message_key
            self._bus.publish(
                ConversionFinished(
                    task_id=job.id, output_path=None, error_key=exc.user_message_key
                )
            )
            logger.error("Conversion %s failed: %s", job.id, exc.detail or exc)
        except Exception:
            job.done = True
            job.error = "error_conversion"
            self._bus.publish(
                ConversionFinished(task_id=job.id, output_path=None, error_key="error_conversion")
            )
            logger.exception("Conversion %s failed unexpectedly", job.id)
        else:
            job.done = True
            job.output_path = output
            self._bus.publish(ConversionFinished(task_id=job.id, output_path=output))
            logger.info("Conversion %s completed: %s", job.id, output)
