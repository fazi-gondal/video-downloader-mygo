package services

import (
	"context"
	"errors"
	"log"
	"sync"

	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

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

// DownloadManager owns the worker pool and task map.
// It never touches UI objects; it only publishes events.
type DownloadManager struct {
	engine     *YtDlpService
	maxWorkers int
	tasks      map[string]*models.DownloadTask
	mu         sync.Mutex
	queue      chan *models.DownloadTask
	events     chan TaskEvent
	shutdown   chan struct{}
	wg         sync.WaitGroup
	started    bool
}

func NewDownloadManager(engine *YtDlpService, maxWorkers int) *DownloadManager {
	if maxWorkers < 1 {
		maxWorkers = 4
	}
	return &DownloadManager{
		engine:     engine,
		maxWorkers: maxWorkers,
		tasks:      make(map[string]*models.DownloadTask),
		queue:      make(chan *models.DownloadTask, 256),
		events:     make(chan TaskEvent, 64),
		shutdown:   make(chan struct{}),
	}
}

// Events returns the channel the UI should range over (on a background goroutine
// that then posts to the main thread).
func (m *DownloadManager) Events() <-chan TaskEvent {
	return m.events
}

func (m *DownloadManager) SetMaxWorkers(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n < 1 {
		n = 1
	}
	m.maxWorkers = n
}

func (m *DownloadManager) ensureWorkers() {
	if m.started {
		return
	}
	m.started = true
	for i := 0; i < m.maxWorkers; i++ {
		m.wg.Add(1)
		go m.worker()
	}
}

func (m *DownloadManager) Enqueue(req models.DownloadRequest) *models.DownloadTask {
	m.ensureWorkers()
	task := models.NewDownloadTask(req)
	m.mu.Lock()
	m.tasks[task.ID] = task
	m.mu.Unlock()
	m.emit(EventTaskQueued, task, nil)
	task.SetState(models.StatePending)
	m.emit(EventTaskState, task, nil)
	select {
	case m.queue <- task:
	default:
		log.Println("download queue full, dropping", task.ID)
	}
	return task
}

func (m *DownloadManager) Cancel(id string) {
	m.mu.Lock()
	task := m.tasks[id]
	m.mu.Unlock()
	if task == nil || task.State.IsTerminal() {
		return
	}
	task.Cancel()
	if task.State == models.StatePending {
		task.SetState(models.StateCancelled)
		m.emit(EventTaskState, task, nil)
	}
}

func (m *DownloadManager) Retry(id string) *models.DownloadTask {
	m.mu.Lock()
	task := m.tasks[id]
	m.mu.Unlock()
	if task == nil {
		return nil
	}
	if task.State != models.StateError && task.State != models.StateCancelled {
		return nil
	}
	task.ResetForRetry()
	m.emit(EventTaskState, task, nil)
	select {
	case m.queue <- task:
	default:
	}
	return task
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

// ClearFinished removes terminal tasks from the in-memory queue. History is
// persisted separately, so clearing the downloads screen does not erase the
// user's history.
func (m *DownloadManager) ClearFinished() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, task := range m.tasks {
		if task.State.IsTerminal() {
			delete(m.tasks, id)
		}
	}
}

func (m *DownloadManager) Shutdown() {
	close(m.shutdown)
	m.wg.Wait()
}

func (m *DownloadManager) worker() {
	defer m.wg.Done()
	for {
		select {
		case <-m.shutdown:
			return
		case task := <-m.queue:
			if task == nil {
				continue
			}
			if task.Cancelled() {
				task.SetState(models.StateCancelled)
				m.emit(EventTaskState, task, nil)
				continue
			}
			m.runTask(task)
		}
	}
}

func (m *DownloadManager) runTask(task *models.DownloadTask) {
	task.SetState(models.StatePreparing)
	m.emit(EventTaskState, task, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-task.Done():
			cancel()
		case <-watchDone:
		}
	}()

	task.SetState(models.StateDownloading)
	m.emit(EventTaskState, task, nil)

	path, err := m.engine.Download(ctx, task, func(p models.ProgressInfo) {
		task.SetProgress(p)
		m.emit(EventTaskProgress, task, nil)
	})

	if err != nil {
		cancelled := task.Cancelled() || errors.Is(err, context.Canceled)
		if cancelled {
			task.SetState(models.StateCancelled)
		} else {
			task.Error = err.Error()
			task.SetState(models.StateError)
		}
		if cancelled {
			m.emit(EventTaskState, task, nil)
			m.emit(EventTaskFinished, task, nil)
		} else {
			m.emit(EventTaskState, task, err)
			m.emit(EventTaskFinished, task, err)
		}
		return
	}

	task.OutputPath = path
	task.SetState(models.StateCompleted)
	m.emit(EventTaskState, task, nil)
	m.emit(EventTaskFinished, task, nil)
}

func (m *DownloadManager) emit(kind EventKind, task *models.DownloadTask, err error) {
	select {
	case m.events <- TaskEvent{Kind: kind, Task: task, Error: err}:
	default:
		// drop if UI is not consuming
	}
}
