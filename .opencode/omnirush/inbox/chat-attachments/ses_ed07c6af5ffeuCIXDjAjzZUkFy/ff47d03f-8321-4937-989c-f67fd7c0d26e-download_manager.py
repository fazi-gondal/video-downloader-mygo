"""Download queue and worker pool.

Workers are plain daemon threads (downloads must outlive any UI handler).
All state changes are published on the :class:`EventBus`; the manager never
touches Flet objects.
"""

from __future__ import annotations

import logging
import queue
import threading
from pathlib import Path
from typing import Protocol

from video_downloader.config.constants import DEFAULT_MAX_CONCURRENT
from video_downloader.core.errors import AppError, DownloadCancelled
from video_downloader.core.event_bus import EventBus
from video_downloader.core.events import TaskQueued, TaskStateChanged
from video_downloader.models.download import (
    DownloadRequest,
    DownloadState,
    DownloadTask,
    ProgressInfo,
)

logger = logging.getLogger(__name__)


class DownloadEngine(Protocol):
    """What the manager needs from a downloader (YtdlpService or a fake)."""

    def download(self, task: DownloadTask, bus: EventBus) -> Path: ...

    @staticmethod
    def cleanup_partials(output_dir: Path, media_id: str) -> None: ...


class DownloadManager:
    def __init__(
        self,
        engine: DownloadEngine,
        bus: EventBus,
        max_concurrent: int = DEFAULT_MAX_CONCURRENT,
    ) -> None:
        self._engine = engine
        self._bus = bus
        self._queue: queue.Queue[DownloadTask | None] = queue.Queue()
        self._tasks: dict[str, DownloadTask] = {}
        self._tasks_lock = threading.Lock()
        self._workers: list[threading.Thread] = []
        self._max_concurrent = max(1, max_concurrent)
        self._shutdown = threading.Event()

    # ------------------------------------------------------------------
    # Public API (called from the UI loop)

    def enqueue(self, request: DownloadRequest) -> DownloadTask:
        self._ensure_workers()
        task = DownloadTask(request=request)
        with self._tasks_lock:
            self._tasks[task.id] = task
        self._bus.publish(TaskQueued(task_id=task.id))
        self._set_state(task, DownloadState.PENDING)
        self._queue.put(task)
        logger.info("Queued task %s: %s", task.id, request.url)
        return task

    def cancel(self, task_id: str) -> None:
        task = self.get(task_id)
        if task is None or task.is_terminal:
            return
        task.cancel_event.set()
        # Pending tasks won't be picked up: workers skip cancelled tasks.
        if task.state is DownloadState.PENDING:
            self._set_state(task, DownloadState.CANCELLED)

    def retry(self, task_id: str) -> DownloadTask | None:
        """Reset a failed/cancelled task in place and re-enqueue it.

        The same task (and therefore the same UI tile and history row) goes
        back to PENDING instead of spawning a duplicate.
        """
        task = self.get(task_id)
        if task is None or task.state not in (DownloadState.ERROR, DownloadState.CANCELLED):
            return None
        task.cancel_event = threading.Event()  # the old one may be set
        task.error = None
        task.error_detail = None
        task.progress = ProgressInfo()
        task.output_path = None
        task.finished_at = None
        self._set_state(task, DownloadState.PENDING)
        self._queue.put(task)
        logger.info("Retrying task %s: %s", task.id, task.request.url)
        return task

    def get(self, task_id: str) -> DownloadTask | None:
        with self._tasks_lock:
            return self._tasks.get(task_id)

    def tasks(self) -> list[DownloadTask]:
        with self._tasks_lock:
            return list(self._tasks.values())

    def set_max_concurrent(self, n: int) -> None:
        self._max_concurrent = max(1, n)
        self._ensure_workers()
        # Shrinking happens lazily: extra workers exit when they see the flag.

    def clear_finished(self) -> None:
        with self._tasks_lock:
            self._tasks = {tid: t for tid, t in self._tasks.items() if not t.is_terminal}

    def shutdown(self, wait: bool = False) -> None:
        self._shutdown.set()
        for task in self.tasks():
            if task.is_active:
                task.cancel_event.set()
        for _ in self._workers:
            self._queue.put(None)
        if wait:
            for worker in self._workers:
                worker.join(timeout=10)

    # ------------------------------------------------------------------
    # Workers

    def _ensure_workers(self) -> None:
        self._workers = [w for w in self._workers if w.is_alive()]
        while len(self._workers) < self._max_concurrent:
            index = len(self._workers)
            worker = threading.Thread(
                target=self._worker_loop,
                args=(index,),
                name=f"download-worker-{index}",
                daemon=True,
            )
            self._workers.append(worker)
            worker.start()

    def _worker_loop(self, slot: int) -> None:
        while not self._shutdown.is_set():
            # Lazy pool shrink: workers beyond the current limit exit.
            if slot >= self._max_concurrent:
                return
            try:
                task = self._queue.get(timeout=1.0)
            except queue.Empty:
                continue
            if task is None:
                return
            if not self._try_claim(task):
                continue
            self._run_task(task)

    def _try_claim(self, task: DownloadTask) -> bool:
        """Atomically claim a queued task; stale/duplicate entries are skipped.

        A task can appear twice in the queue (cancelled while pending, then
        retried); only the entry that flips PENDING -> PREPARING runs.
        """
        with self._tasks_lock:
            if task.state is not DownloadState.PENDING or task.cancel_event.is_set():
                return False
            task.state = DownloadState.PREPARING
        self._bus.publish(
            TaskStateChanged(task_id=task.id, state=DownloadState.PREPARING)
        )
        return True

    def _run_task(self, task: DownloadTask) -> None:
        try:
            output_path = self._engine.download(task, self._bus)
        except DownloadCancelled:
            self._engine.cleanup_partials(task.request.output_dir, self._media_id_hint(task))
            self._set_state(task, DownloadState.CANCELLED)
            logger.info("Task %s cancelled", task.id)
        except AppError as exc:
            task.error = exc.user_message_key
            task.error_detail = exc.detail or str(exc)
            self._set_state(task, DownloadState.ERROR, error_key=exc.user_message_key)
            logger.error("Task %s failed: %s", task.id, exc.detail or exc)
        except Exception as exc:
            task.error = "error_generic"
            task.error_detail = str(exc)
            self._set_state(task, DownloadState.ERROR, error_key="error_generic")
            logger.exception("Task %s failed unexpectedly", task.id)
        else:
            task.output_path = output_path
            self._set_state(task, DownloadState.COMPLETED, output_path=output_path)
            logger.info("Task %s completed: %s", task.id, output_path)

    # ------------------------------------------------------------------

    def _set_state(
        self,
        task: DownloadTask,
        state: DownloadState,
        error_key: str | None = None,
        output_path: Path | None = None,
    ) -> None:
        task.state = state
        if task.is_terminal:
            from datetime import datetime

            task.finished_at = datetime.now()
        self._bus.publish(
            TaskStateChanged(
                task_id=task.id, state=state, error_key=error_key, output_path=output_path
            )
        )

    @staticmethod
    def _media_id_hint(task: DownloadTask) -> str:
        # Output template embeds the media id in brackets; use URL tail as hint
        url = task.request.url
        for marker in ("v=", "/"):
            if marker in url:
                candidate = url.rsplit(marker, 1)[-1].split("&")[0].split("?")[0]
                if candidate:
                    return candidate
        return ""
