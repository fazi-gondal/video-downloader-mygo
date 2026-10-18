package services

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

// ErrConversionServiceClosed is returned when a job is submitted after
// shutdown has started.
var ErrConversionServiceClosed = errors.New("conversion service is shut down")

const maxRetainedConversionJobs = 100

type ConversionEventKind int

const (
	ConvEventQueued ConversionEventKind = iota
	ConvEventProgress
	ConvEventFinished
)

type ConversionEvent struct {
	Kind ConversionEventKind
	Job  *models.ConversionJob
	Err  error
}

type conversionRunner interface {
	Convert(context.Context, *models.ConversionJob, func(float64)) (string, error)
}

// ConversionService is a bounded single-worker queue for FFmpeg jobs.
type ConversionService struct {
	ffmpeg       conversionRunner
	queue        chan *models.ConversionJob
	jobs         map[string]*models.ConversionJob
	mu           sync.Mutex
	events       chan ConversionEvent
	ctx          context.Context
	cancel       context.CancelFunc
	shutdown     chan struct{}
	started      bool
	stopped      bool
	wg           sync.WaitGroup
	shutdownOnce sync.Once
}

func NewConversionService(ffmpeg *FFmpegService) *ConversionService {
	ctx, cancel := context.WithCancel(context.Background())
	return &ConversionService{
		ffmpeg:   ffmpeg,
		queue:    make(chan *models.ConversionJob, 64),
		jobs:     make(map[string]*models.ConversionJob),
		events:   make(chan ConversionEvent, 256),
		ctx:      ctx,
		cancel:   cancel,
		shutdown: make(chan struct{}),
	}
}

func (s *ConversionService) Events() <-chan ConversionEvent {
	return s.events
}

// Enqueue waits for bounded queue capacity for compatibility with existing
// callers. Use EnqueueContext when a caller needs cancellation while applying
// backpressure.
func (s *ConversionService) Enqueue(req models.ConversionRequest) *models.ConversionJob {
	job, _ := s.EnqueueContext(context.Background(), req)
	return job
}

// EnqueueContext returns a job only after it has been accepted by the queue.
// Queue saturation is handled by waiting for capacity or returning ctx.Err();
// no job is silently discarded.
func (s *ConversionService) EnqueueContext(ctx context.Context, req models.ConversionRequest) (*models.ConversionJob, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.ensureWorker() {
		return nil, ErrConversionServiceClosed
	}

	job := models.NewConversionJob(req)
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil, ErrConversionServiceClosed
	}
	s.jobs[job.ID] = job
	s.mu.Unlock()

	select {
	case s.queue <- job:
		s.emit(ConvEventQueued, job, nil)
		return job, nil
	case <-ctx.Done():
		s.removeUnaccepted(job)
		return nil, ctx.Err()
	case <-s.shutdown:
		s.removeUnaccepted(job)
		return nil, ErrConversionServiceClosed
	}
}

func (s *ConversionService) removeUnaccepted(job *models.ConversionJob) {
	s.mu.Lock()
	delete(s.jobs, job.ID)
	s.mu.Unlock()
}

func (s *ConversionService) Cancel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job != nil && !job.StateValue().IsTerminal() {
		job.Cancel()
	}
}

func (s *ConversionService) All() []*models.ConversionJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*models.ConversionJob, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j)
	}
	return out
}

// Snapshots returns a consistent view of every conversion job for the UI.
func (s *ConversionService) Snapshots() []models.ConversionJobSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]models.ConversionJobSnapshot, 0, len(s.jobs))
	for _, job := range s.jobs {
		out = append(out, job.Snapshot())
	}
	return out
}

func (s *ConversionService) ensureWorker() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return false
	}
	if s.started {
		return true
	}
	s.started = true
	s.wg.Add(1)
	go s.worker()
	return true
}

// Shutdown is idempotent, cancels the service context (including an active
// FFmpeg process), waits for the worker and then finalizes any queued jobs.
func (s *ConversionService) Shutdown() {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		close(s.shutdown)
		s.mu.Unlock()

		s.cancel()
		s.wg.Wait()

		s.mu.Lock()
		deferred := make([]*models.ConversionJob, 0, len(s.jobs))
		for _, job := range s.jobs {
			if !job.StateValue().IsTerminal() {
				deferred = append(deferred, job)
			}
		}
		s.mu.Unlock()
		for _, job := range deferred {
			job.Cancel()
			s.finishCancelled(job)
		}
		close(s.events)
	})
}

func (s *ConversionService) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.shutdown:
			return
		case job := <-s.queue:
			if job == nil {
				continue
			}
			if s.isStopped() || job.Cancelled() {
				if !job.StateValue().IsTerminal() {
					s.finishCancelled(job)
				}
				continue
			}
			s.runJob(job)
		}
	}
}

func (s *ConversionService) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

func (s *ConversionService) runJob(job *models.ConversionJob) {
	job.SetState(models.ConvRunning)
	s.emit(ConvEventProgress, job, nil)

	ctx, cancel := context.WithCancel(s.ctx)
	watchDone := make(chan struct{})
	watchExited := make(chan struct{})
	go func() {
		defer close(watchExited)
		select {
		case <-job.Done():
			cancel()
		case <-watchDone:
		}
	}()

	var path string
	var err error
	if s.ffmpeg == nil {
		err = errors.New("ffmpeg service is unavailable")
	} else {
		path, err = s.ffmpeg.Convert(ctx, job, func(pct float64) {
			job.SetProgress(pct)
			s.emit(ConvEventProgress, job, nil)
		})
	}
	close(watchDone)
	cancel()
	<-watchExited

	if err != nil || job.Cancelled() || s.isStopped() {
		if job.Cancelled() || errors.Is(err, context.Canceled) || s.isStopped() {
			s.finishCancelled(job)
		} else {
			now := time.Now()
			job.SetFinishedAt(now)
			job.SetState(models.ConvError)
			job.SetError(err.Error())
			s.emit(ConvEventFinished, job, err)
			s.pruneFinished()
		}
		return
	}

	job.SetOutput(path)
	job.SetProgress(100)
	job.SetState(models.ConvCompleted)
	now := time.Now()
	job.SetFinishedAt(now)
	s.emit(ConvEventFinished, job, nil)
	s.pruneFinished()
}

func (s *ConversionService) finishCancelled(job *models.ConversionJob) {
	job.SetState(models.ConvCancelled)
	now := time.Now()
	job.SetFinishedAt(now)
	s.emit(ConvEventFinished, job, nil)
	s.pruneFinished()
}

func (s *ConversionService) pruneFinished() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.jobs) > maxRetainedConversionJobs {
		oldestID := ""
		var oldest time.Time
		for id, job := range s.jobs {
			snapshot := job.Snapshot()
			if !snapshot.State.IsTerminal() {
				continue
			}
			finished := snapshot.CreatedAt
			if snapshot.FinishedAt != nil {
				finished = *snapshot.FinishedAt
			}
			if oldestID == "" || finished.Before(oldest) {
				oldestID = id
				oldest = finished
			}
		}
		if oldestID == "" {
			return
		}
		delete(s.jobs, oldestID)
	}
}

func (s *ConversionService) emit(kind ConversionEventKind, job *models.ConversionJob, err error) {
	if kind == ConvEventProgress {
		select {
		case s.events <- ConversionEvent{Kind: kind, Job: job, Err: err}:
		default:
		}
		return
	}
	select {
	case s.events <- ConversionEvent{Kind: kind, Job: job, Err: err}:
	case <-s.shutdown:
	}
}
