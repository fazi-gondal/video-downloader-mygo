package services

import (
	"context"
	"errors"
	"sync"

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

type downloadEngine interface {
	Download(context.Context, *models.DownloadTask, func(models.ProgressInfo)) (string, error)
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
	queue        chan *models.DownloadTask
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
		queue:      make(chan *models.DownloadTask, 256),
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
	case m.queue <- task:
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
	if task.StateValue() == models.StatePending {
		task.SetState(models.StateCancelled)
		m.emit(EventTaskState, task, nil)
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
	case m.queue <- task:
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
// marks queued tasks cancelled, and waits for every worker to exit.
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
			if task.StateValue().IsTerminal() {
				continue
			}
			task.Cancel()
			if task.StateValue() == models.StatePending {
				task.SetState(models.StateCancelled)
				m.emit(EventTaskState, task, nil)
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
		case task := <-m.queue:
			if task == nil {
				continue
			}
			if task.Cancelled() {
				if !task.StateValue().IsTerminal() {
					task.SetState(models.StateCancelled)
					m.emit(EventTaskState, task, nil)
				}
				continue
			}
			if m.isStopped() {
				task.Cancel()
				if !task.StateValue().IsTerminal() {
					task.SetState(models.StateCancelled)
					m.emit(EventTaskState, task, nil)
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
		task.SetProgress(p)
		m.emit(EventTaskProgress, task, nil)
	})

	if err != nil || task.Cancelled() || m.isStopped() {
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
