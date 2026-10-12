package services

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

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

// ConversionService is a single-worker queue for FFmpeg jobs.
type ConversionService struct {
	ffmpeg   *FFmpegService
	queue    chan *models.ConversionJob
	jobs     map[string]*models.ConversionJob
	mu       sync.Mutex
	events   chan ConversionEvent
	shutdown chan struct{}
	started  bool
}

func NewConversionService(ffmpeg *FFmpegService) *ConversionService {
	return &ConversionService{
		ffmpeg:   ffmpeg,
		queue:    make(chan *models.ConversionJob, 64),
		jobs:     make(map[string]*models.ConversionJob),
		events:   make(chan ConversionEvent, 32),
		shutdown: make(chan struct{}),
	}
}

func (s *ConversionService) Events() <-chan ConversionEvent {
	return s.events
}

func (s *ConversionService) Enqueue(req models.ConversionRequest) *models.ConversionJob {
	s.ensureWorker()
	job := models.NewConversionJob(req)
	s.mu.Lock()
	s.jobs[job.ID] = job
	s.mu.Unlock()
	s.emit(ConvEventQueued, job, nil)
	select {
	case s.queue <- job:
	default:
		log.Println("conversion queue full")
	}
	return job
}

func (s *ConversionService) Cancel(id string) {
	s.mu.Lock()
	job := s.jobs[id]
	s.mu.Unlock()
	if job != nil && !job.State.IsTerminal() {
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

func (s *ConversionService) ensureWorker() {
	if s.started {
		return
	}
	s.started = true
	go s.worker()
}

func (s *ConversionService) worker() {
	for {
		select {
		case <-s.shutdown:
			return
		case job := <-s.queue:
			if job == nil {
				continue
			}
			if job.Cancelled() {
				job.State = models.ConvCancelled
				now := time.Now()
				job.FinishedAt = &now
				s.emit(ConvEventFinished, job, nil)
				continue
			}
			job.State = models.ConvRunning
			s.emit(ConvEventProgress, job, nil)

			ctx, cancel := context.WithCancel(context.Background())
			watchDone := make(chan struct{})
			go func() {
				select {
				case <-job.Done():
					cancel()
				case <-watchDone:
				}
			}()

			path, err := s.ffmpeg.Convert(ctx, job, func(pct float64) {
				job.Percent = pct
				s.emit(ConvEventProgress, job, nil)
			})
			close(watchDone)
			cancel()

			if err != nil {
				cancelled := job.Cancelled() || err == context.Canceled
				now := time.Now()
				job.FinishedAt = &now
				if cancelled {
					job.State = models.ConvCancelled
					err = nil
				} else {
					job.State = models.ConvError
					job.Error = err.Error()
				}
				s.emit(ConvEventFinished, job, err)
				continue
			}
			job.OutputPath = path
			job.Percent = 100
			job.State = models.ConvCompleted
			now := time.Now()
			job.FinishedAt = &now
			s.emit(ConvEventFinished, job, nil)
		}
	}
}

func (s *ConversionService) emit(kind ConversionEventKind, job *models.ConversionJob, err error) {
	select {
	case s.events <- ConversionEvent{Kind: kind, Job: job, Err: err}:
	default:
	}
}

func (s *ConversionService) Shutdown() {
	close(s.shutdown)
}
