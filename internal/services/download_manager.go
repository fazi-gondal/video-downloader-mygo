package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

// ErrDownloadManagerClosed is returned when a task is submitted after the
// manager has begun shutting down.
var ErrDownloadManagerClosed = errors.New("download manager is shut down")

// Event kinds the UI listens to.
type EventKind int

const (
	EventTaskQueued EventKind = iota
	EventTaskState
	EventTaskProgress
	EventTaskFinished
)

type TaskEvent struct {
	Kind  EventKind
	Task  *models.DownloadTask
	Error error
}

type queuedDownload struct {
	task       *models.DownloadTask
	generation uint64
}

type downloadEngine interface {
	Download(context.Context, *models.DownloadTask, func(models.ProgressInfo)) (string, error)
}

type partialDownloadCleaner interface {
	DiscardPartial(*models.DownloadTask)
}

// DownloadManager owns the bounded worker pool and task map.
// It never touches UI objects; it only publishes events.
type DownloadManager struct {
	engine       downloadEngine
	maxWorkers   int
	workerCount  int
	started      bool
	stopped      bool
	tasks        map[string]*models.DownloadTask
	mu           sync.Mutex
	queue        chan queuedDownload
	events       chan TaskEvent
	shutdown     chan struct{}
	resize       chan struct{}
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	shutdownOnce sync.Once
}

func NewDownloadManager(engine *YtDlpService, maxWorkers int) *DownloadManager {
	if maxWorkers < 1 {
		maxWorkers = 4
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &DownloadManager{
		engine:     engine,
		maxWorkers: maxWorkers,
		tasks:      make(map[string]*models.DownloadTask),
		queue:      make(chan queuedDownload, 256),
		events:     make(chan TaskEvent, 2048),
		shutdown:   make(chan struct{}),
		resize:     make(chan struct{}),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Events returns the channel the UI should range over (on a background
// goroutine that then posts to the main thread).
func (m *DownloadManager) Events() <-chan TaskEvent {
	return m.events
}

// SetMaxWorkers changes the target pool size. Workers already running a
// download finish that download before excess workers leave; idle workers
// resize immediately. Tasks remain in the bounded queue throughout a resize.
func (m *DownloadManager) SetMaxWorkers(n int) {
	if n < 1 {
		n = 1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxWorkers = n
	if m.started && !m.stopped {
		m.startWorkersLocked()
		m.signalResizeLocked()
	}
}

// WorkerCount reports the current number of live workers. It is useful for
// observability and makes SetMaxWorkers' eventual resize behavior explicit.
func (m *DownloadManager) WorkerCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workerCount
}

func (m *DownloadManager) startWorkersLocked() {
	for m.workerCount < m.maxWorkers {
		m.workerCount++
		m.wg.Add(1)
		go m.worker()
	}
}

func (m *DownloadManager) signalResizeLocked() {
	close(m.resize)
	m.resize = make(chan struct{})
}

func (m *DownloadManager) ensureWorkers() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return false
	}
	if !m.started {
		m.started = true
	}
	m.startWorkersLocked()
	return true
}

// Enqueue submits a task using an unbounded caller context for compatibility
// with existing UI callers. It waits for queue capacity instead of dropping a
// task. New callers should use EnqueueContext to make backpressure explicit.
func (m *DownloadManager) Enqueue(req models.DownloadRequest) *models.DownloadTask {
	task, _ := m.EnqueueContext(context.Background(), req)
	return task
}

// EnqueueContext waits for bounded queue capacity or returns ctx.Err(). A
// non-nil task is returned only after it has been accepted by the queue.
func (m *DownloadManager) EnqueueContext(ctx context.Context, req models.DownloadRequest) (*models.DownloadTask, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !m.ensureWorkers() {
		return nil, ErrDownloadManagerClosed
	}

	task := models.NewDownloadTask(req)
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return nil, ErrDownloadManagerClosed
	}
	m.tasks[task.ID] = task
	m.mu.Unlock()

	select {
	case m.queue <- queuedDownload{task: task, generation: task.QueueGeneration()}:
		m.emit(EventTaskQueued, task, nil)
		m.emit(EventTaskState, task, nil)
		return task, nil
	case <-ctx.Done():
		m.removeUnaccepted(task)
		return nil, ctx.Err()
	case <-m.shutdown:
		m.removeUnaccepted(task)
		return nil, ErrDownloadManagerClosed
	}
}

func (m *DownloadManager) removeUnaccepted(task *models.DownloadTask) {
	m.mu.Lock()
	delete(m.tasks, task.ID)
	m.mu.Unlock()
}

func (m *DownloadManager) Cancel(id string) {
	m.mu.Lock()
	task := m.tasks[id]
	m.mu.Unlock()
	if task == nil || task.StateValue().IsTerminal() {
		return
	}
	task.Cancel()
	state := task.StateValue()
	if state == models.StatePaused {
		if cleaner, ok := m.engine.(partialDownloadCleaner); ok {
			cleaner.DiscardPartial(task)
		}
		task.SetState(models.StateCancelled)
		m.emit(EventTaskState, task, nil)
		m.emit(EventTaskFinished, task, nil)
	} else if state == models.StatePending {
		task.SetState(models.StateCancelled)
		m.emit(EventTaskState, task, nil)
		m.emit(EventTaskFinished, task, nil)
	}
}

// Pause stops an active or queued task while keeping its partial files for a
// later Resume call.
func (m *DownloadManager) Pause(id string) {
	m.mu.Lock()
	task := m.tasks[id]
	m.mu.Unlock()
	if task == nil || task.StateValue().IsTerminal() || task.StateValue() == models.StatePaused {
		return
	}
	task.RequestPause()
	if task.StateValue() == models.StatePending {
		task.SetState(models.StatePaused)
		m.emit(EventTaskState, task, nil)
	}
}

func (m *DownloadManager) Resume(id string) *models.DownloadTask {
	task, _ := m.ResumeContext(context.Background(), id)
	return task
}

func (m *DownloadManager) ResumeContext(ctx context.Context, id string) (*models.DownloadTask, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !m.ensureWorkers() {
		return nil, ErrDownloadManagerClosed
	}

	m.mu.Lock()
	task := m.tasks[id]
	if task == nil {
		m.mu.Unlock()
		return nil, errors.New("download task not found")
	}
	if task.StateValue() != models.StatePaused {
		m.mu.Unlock()
		return nil, errors.New("download task is not paused")
	}
	task.ResetForResume()
	m.mu.Unlock()

	select {
	case m.queue <- queuedDownload{task: task, generation: task.QueueGeneration()}:
		m.emit(EventTaskState, task, nil)
		return task, nil
	case <-ctx.Done():
		task.RequestPause()
		task.SetState(models.StatePaused)
		return nil, ctx.Err()
	case <-m.shutdown:
		task.RequestPause()
		task.SetState(models.StatePaused)
		return nil, ErrDownloadManagerClosed
	}
}

// Retry keeps the legacy API and waits for queue capacity. New callers that
// need cancellation while waiting should use RetryContext.
func (m *DownloadManager) Retry(id string) *models.DownloadTask {
	task, _ := m.RetryContext(context.Background(), id)
	return task
}

// RetryContext requeues a failed or cancelled task without dropping it when
// the queue is full. A non-nil task is returned only after acceptance.
func (m *DownloadManager) RetryContext(ctx context.Context, id string) (*models.DownloadTask, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !m.ensureWorkers() {
		return nil, ErrDownloadManagerClosed
	}

	m.mu.Lock()
	task := m.tasks[id]
	if task == nil {
		m.mu.Unlock()
		return nil, errors.New("download task not found")
	}
	state := task.StateValue()
	if state != models.StateError && state != models.StateCancelled {
		m.mu.Unlock()
		return nil, errors.New("download task is not retryable")
	}
	previous := task.Snapshot()
	previousState := previous.State
	previousError := previous.Error
	previousDetail := previous.ErrorDetail
	task.ResetForRetry()
	m.mu.Unlock()

	select {
	case m.queue <- queuedDownload{task: task, generation: task.QueueGeneration()}:
		m.emit(EventTaskState, task, nil)
		return task, nil
	case <-ctx.Done():
		task.SetError(previousError)
		task.SetErrorDetail(previousDetail)
		task.SetState(previousState)
		return nil, ctx.Err()
	case <-m.shutdown:
		task.SetError(previousError)
		task.SetErrorDetail(previousDetail)
		task.SetState(previousState)
		return nil, ErrDownloadManagerClosed
	}
}

func (m *DownloadManager) Get(id string) *models.DownloadTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tasks[id]
}

func (m *DownloadManager) All() []*models.DownloadTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*models.DownloadTask, 0, len(m.tasks))
	for _, t := range m.tasks {
		out = append(out, t)
	}
	return out
}

// Snapshots returns a consistent view of every task for UI/persistence code.
func (m *DownloadManager) Snapshots() []models.DownloadTaskSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]models.DownloadTaskSnapshot, 0, len(m.tasks))
	for _, task := range m.tasks {
		out = append(out, task.Snapshot())
	}
	return out
}

// RestorePausedTask adds a task loaded from disk without starting a worker.
func (m *DownloadManager) RestorePausedTask(task *models.DownloadTask) error {
	if task == nil {
		return errors.New("cannot restore a nil download task")
	}
	snapshot := task.Snapshot()
	if snapshot.ID == "" || snapshot.State != models.StatePaused {
		return errors.New("restored download task must have an ID and be paused")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return ErrDownloadManagerClosed
	}
	if _, exists := m.tasks[snapshot.ID]; exists {
		return fmt.Errorf("download task %q is already registered", snapshot.ID)
	}
	m.tasks[snapshot.ID] = task
	return nil
}

// PauseAll requests that active downloads stop while keeping their partial
// files. It returns after every task is paused or terminal.
func (m *DownloadManager) PauseAll(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for _, task := range m.All() {
		m.Pause(task.ID)
	}

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		allStopped := true
		for _, task := range m.Snapshots() {
			if !task.State.IsTerminal() && task.State != models.StatePaused {
				allStopped = false
				break
			}
		}
		if allStopped {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// CancelAll permanently cancels tasks and discards any paused partial files.
func (m *DownloadManager) CancelAll() {
	for _, task := range m.All() {
		m.Cancel(task.ID)
	}
}

// WaitForTerminal waits until no task is active or paused.
func (m *DownloadManager) WaitForTerminal(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		allTerminal := true
		for _, task := range m.Snapshots() {
			if !task.State.IsTerminal() {
				allTerminal = false
				break
			}
		}
		if allTerminal {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// ClearFinished removes terminal tasks from the in-memory queue. History is
// persisted separately, so clearing the downloads screen does not erase the
// user's history.
func (m *DownloadManager) ClearFinished() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, task := range m.tasks {
		if task.StateValue().IsTerminal() {
			delete(m.tasks, id)
		}
	}
}

// Shutdown is safe to call more than once. It cancels active downloads,
// marks queued tasks cancelled, preserves paused tasks, and waits for every
// worker to exit.
func (m *DownloadManager) Shutdown() {
	m.shutdownOnce.Do(func() {
		m.mu.Lock()
		m.stopped = true
		close(m.shutdown)
		close(m.resize)
		tasks := make([]*models.DownloadTask, 0, len(m.tasks))
		for _, task := range m.tasks {
			tasks = append(tasks, task)
		}
		m.mu.Unlock()

		m.cancel()
		for _, task := range tasks {
			state := task.StateValue()
			if task.StateValue().IsTerminal() || state == models.StatePaused {
				continue
			}
			task.Cancel()
			if state == models.StatePending {
				task.SetState(models.StateCancelled)
				m.emit(EventTaskState, task, nil)
				m.emit(EventTaskFinished, task, nil)
			}
		}
		m.wg.Wait()
		close(m.events)
	})
}

func (m *DownloadManager) worker() {
	retired := false
	defer func() {
		if !retired {
			m.mu.Lock()
			m.workerCount--
			m.mu.Unlock()
		}
		m.wg.Done()
	}()

	for {
		if m.retireExcessWorker() {
			retired = true
			return
		}
		m.mu.Lock()
		stopped := m.stopped
		resize := m.resize
		m.mu.Unlock()
		if stopped {
			return
		}

		select {
		case <-m.shutdown:
			return
		case <-resize:
			continue
		case item := <-m.queue:
			task := item.task
			if task == nil {
				continue
			}
			if task.QueueGeneration() != item.generation {
				continue
			}
			if task.Cancelled() {
				if task.StateValue() == models.StatePaused {
					continue
				}
				if !task.StateValue().IsTerminal() {
					task.SetState(models.StateCancelled)
					m.emit(EventTaskState, task, nil)
					m.emit(EventTaskFinished, task, nil)
				}
				continue
			}
			if m.isStopped() {
				task.Cancel()
				if !task.StateValue().IsTerminal() {
					task.SetState(models.StateCancelled)
					m.emit(EventTaskState, task, nil)
					m.emit(EventTaskFinished, task, nil)
				}
				continue
			}
			m.runTask(task)
		}
	}
}

// retireExcessWorker removes exactly one worker from the live count while
// holding the mutex. This prevents every worker from observing the same
// excess count and exiting during a downsize.
func (m *DownloadManager) retireExcessWorker() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.workerCount <= m.maxWorkers {
		return false
	}
	m.workerCount--
	return true
}

func (m *DownloadManager) isStopped() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopped
}

func (m *DownloadManager) runTask(task *models.DownloadTask) {
	task.SetState(models.StatePreparing)
	m.emit(EventTaskState, task, nil)
	if task.Request.ExpectedSizeBytes > 0 {
		task.SetProgress(models.ProgressInfo{
			TotalBytes:      task.Request.ExpectedSizeBytes,
			TotalIsEstimate: task.Request.ExpectedSizeIsApproximate,
		})
		m.emit(EventTaskProgress, task, nil)
	}

	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	watchDone := make(chan struct{})
	watchExited := make(chan struct{})
	go func() {
		defer close(watchExited)
		select {
		case <-task.Done():
			cancel()
		case <-watchDone:
		}
	}()
	defer func() {
		close(watchDone)
		cancel()
		<-watchExited
	}()

	task.SetState(models.StateDownloading)
	m.emit(EventTaskState, task, nil)

	if m.engine == nil {
		m.finishTaskError(task, errors.New("yt-dlp service is unavailable"))
		return
	}
	path, err := m.engine.Download(ctx, task, func(p models.ProgressInfo) {
		if p.Status == "finished" {
			if task.StateValue() != models.StateProcessing {
				task.SetState(models.StateProcessing)
				m.emit(EventTaskState, task, nil)
			}
		} else if p.Status == "downloading" && task.StateValue() == models.StateProcessing {
			// yt-dlp can finish one requested stream before starting another.
			// Return to downloading until the process itself exits.
			task.SetState(models.StateDownloading)
			m.emit(EventTaskState, task, nil)
		}
		if p.TotalBytes <= 0 && task.Request.ExpectedSizeBytes > 0 {
			p.TotalBytes = task.Request.ExpectedSizeBytes
			p.TotalIsEstimate = task.Request.ExpectedSizeIsApproximate
			if p.TotalBytes > 0 && p.DownloadedBytes >= 0 {
				p.Percent = float64(p.DownloadedBytes) / float64(p.TotalBytes) * 100
			}
		}
		task.SetProgress(p)
		m.emit(EventTaskProgress, task, nil)
	})

	if err != nil || task.Cancelled() || m.isStopped() {
		if task.PauseRequested() && !m.isStopped() {
			task.SetState(models.StatePaused)
			m.emit(EventTaskState, task, nil)
			return
		}
		cancelled := task.Cancelled() || errors.Is(err, context.Canceled) || m.isStopped()
		if cancelled {
			task.SetState(models.StateCancelled)
			m.emit(EventTaskState, task, nil)
			m.emit(EventTaskFinished, task, nil)
		} else {
			m.finishTaskError(task, err)
		}
		return
	}

	task.SetOutputPath(path)
	task.SetState(models.StateCompleted)
	m.emit(EventTaskState, task, nil)
	m.emit(EventTaskFinished, task, nil)
}

func (m *DownloadManager) finishTaskError(task *models.DownloadTask, err error) {
	if err == nil {
		err = errors.New("download failed")
	}
	task.SetError(err.Error())
	task.SetState(models.StateError)
	m.emit(EventTaskState, task, err)
	m.emit(EventTaskFinished, task, err)
}

func (m *DownloadManager) emit(kind EventKind, task *models.DownloadTask, err error) {
	// Progress is intentionally lossy: it can be emitted many times per
	// second. Queue/state/finished events are bounded by the task queue and
	// must remain observable so history and the UI cannot miss a terminal job.
	if kind == EventTaskProgress {
		select {
		case m.events <- TaskEvent{Kind: kind, Task: task, Error: err}:
		default:
		}
		return
	}
	select {
	case m.events <- TaskEvent{Kind: kind, Task: task, Error: err}:
	case <-m.shutdown:
	}
}
