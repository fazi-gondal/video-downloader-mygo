package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/config"
	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

type blockingDownloadEngine struct {
	started chan struct{}
}

func (e *blockingDownloadEngine) Download(ctx context.Context, _ *models.DownloadTask, _ func(models.ProgressInfo)) (string, error) {
	select {
	case <-e.started:
	default:
		close(e.started)
	}
	<-ctx.Done()
	return "", ctx.Err()
}

type progressDownloadEngine struct {
	processing chan bool
}

func (e progressDownloadEngine) Download(_ context.Context, task *models.DownloadTask, onProgress func(models.ProgressInfo)) (string, error) {
	onProgress(models.ProgressInfo{
		Status: "downloading", DownloadedBytes: 25, TotalBytes: 100, Percent: 25,
	})
	onProgress(models.ProgressInfo{
		Status: "finished", DownloadedBytes: 100, TotalBytes: 100, Percent: 100,
	})
	e.processing <- task.StateValue() == models.StateProcessing
	return "output.mp4", nil
}

func TestDownloadManagerTransitionsToProcessingAfterStreamFinishes(t *testing.T) {
	manager := NewDownloadManager(&YtDlpService{}, 1)
	engine := progressDownloadEngine{processing: make(chan bool, 1)}
	manager.engine = engine
	task, err := manager.EnqueueContext(context.Background(), models.DownloadRequest{URL: "https://example.test/video"})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown()

	deadline := time.After(time.Second)
	for {
		select {
		case event := <-manager.Events():
			if event.Task == nil || event.Task.ID != task.ID {
				continue
			}
			if event.Kind == EventTaskFinished {
				if !<-engine.processing {
					t.Fatal("manager did not enter processing state after stream finished")
				}
				if got := task.Snapshot().Progress.Percent; got != 100 {
					t.Fatalf("final progress percent = %v, want 100", got)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for download to finish")
		}
	}
}

func TestDownloadManagerEnqueueContextNeverAcceptsAnUnqueuedTask(t *testing.T) {
	engine := &blockingDownloadEngine{started: make(chan struct{})}
	manager := NewDownloadManager(&YtDlpService{}, 1)
	manager.engine = engine

	first, err := manager.EnqueueContext(context.Background(), models.DownloadRequest{URL: "first"})
	if err != nil || first == nil {
		t.Fatalf("first enqueue = (%v, %v), want accepted task", first, err)
	}
	select {
	case <-engine.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start the first task")
	}

	for i := 0; i < cap(manager.queue); i++ {
		if _, err := manager.EnqueueContext(context.Background(), models.DownloadRequest{URL: "queued"}); err != nil {
			t.Fatalf("fill enqueue %d failed: %v", i, err)
		}
	}
	accepted := len(manager.All())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if task, err := manager.EnqueueContext(ctx, models.DownloadRequest{URL: "rejected"}); task != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("saturated enqueue = (%v, %v), want nil and deadline exceeded", task, err)
	}
	if got := len(manager.All()); got != accepted {
		t.Fatalf("task map size after rejected enqueue = %d, want %d", got, accepted)
	}

	manager.Shutdown()
	manager.Shutdown()
}

func TestDownloadManagerShutdownCancelsWorkAndIsIdempotent(t *testing.T) {
	engine := &blockingDownloadEngine{started: make(chan struct{})}
	manager := NewDownloadManager(&YtDlpService{}, 1)
	manager.engine = engine
	task, err := manager.EnqueueContext(context.Background(), models.DownloadRequest{URL: "cancel"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-engine.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}

	done := make(chan struct{})
	go func() {
		manager.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not wait for the in-flight worker")
	}
	manager.Shutdown()
	if !task.State.IsTerminal() || task.State != models.StateCancelled {
		t.Fatalf("task state after shutdown = %q, want cancelled", task.State)
	}
	if got := manager.WorkerCount(); got != 0 {
		t.Fatalf("live workers after shutdown = %d, want 0", got)
	}
	if _, err := manager.EnqueueContext(context.Background(), models.DownloadRequest{}); !errors.Is(err, ErrDownloadManagerClosed) {
		t.Fatalf("enqueue after shutdown error = %v, want ErrDownloadManagerClosed", err)
	}
}

func TestDownloadManagerCancelQueuedTaskEmitsFinished(t *testing.T) {
	engine := &blockingDownloadEngine{started: make(chan struct{})}
	manager := NewDownloadManager(&YtDlpService{}, 1)
	manager.engine = engine
	first, err := manager.EnqueueContext(context.Background(), models.DownloadRequest{URL: "running"})
	if err != nil || first == nil {
		t.Fatalf("first enqueue = (%v, %v)", first, err)
	}
	select {
	case <-engine.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	queued, err := manager.EnqueueContext(context.Background(), models.DownloadRequest{URL: "queued"})
	if err != nil || queued == nil {
		t.Fatalf("queued enqueue = (%v, %v)", queued, err)
	}
	manager.Cancel(queued.ID)

	foundFinished := false
	deadline := time.After(time.Second)
	for !foundFinished {
		select {
		case event := <-manager.Events():
			if event.Kind == EventTaskFinished && event.Task != nil && event.Task.ID == queued.ID {
				foundFinished = true
			}
		case <-deadline:
			t.Fatal("queued cancellation did not emit a finished event")
		}
	}
	manager.Shutdown()
}

func TestDownloadManagerResizesWorkerPool(t *testing.T) {
	manager := NewDownloadManager(&YtDlpService{settings: func() config.AppSettings { return config.AppSettings{} }}, 1)
	manager.engine = &blockingDownloadEngine{started: make(chan struct{})}
	if _, err := manager.EnqueueContext(context.Background(), models.DownloadRequest{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for manager.WorkerCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	manager.SetMaxWorkers(3)
	deadline = time.Now().Add(time.Second)
	for manager.WorkerCount() != 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := manager.WorkerCount(); got != 3 {
		t.Fatalf("workers after resize = %d, want 3", got)
	}
	manager.SetMaxWorkers(1)
	deadline = time.Now().Add(time.Second)
	for manager.WorkerCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := manager.WorkerCount(); got != 1 {
		t.Fatalf("workers after downsize = %d, want 1", got)
	}
	manager.Shutdown()
}
