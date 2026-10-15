package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

type blockingConversionRunner struct {
	started chan struct{}
}

func (r *blockingConversionRunner) Convert(ctx context.Context, _ *models.ConversionJob, _ func(float64)) (string, error) {
	select {
	case <-r.started:
	default:
		close(r.started)
	}
	<-ctx.Done()
	return "", ctx.Err()
}

func TestConversionShutdownWaitsCancelsAndIsIdempotent(t *testing.T) {
	runner := &blockingConversionRunner{started: make(chan struct{})}
	service := NewConversionService(&FFmpegService{})
	service.ffmpeg = runner
	job, err := service.EnqueueContext(context.Background(), models.ConversionRequest{Source: "source"})
	if err != nil || job == nil {
		t.Fatalf("enqueue = (%v, %v), want accepted job", job, err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("conversion worker did not start")
	}

	done := make(chan struct{})
	go func() {
		service.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not wait for the in-flight conversion")
	}
	service.Shutdown()
	if job.State != models.ConvCancelled {
		t.Fatalf("job state after shutdown = %q, want cancelled", job.State)
	}
	if job.FinishedAt == nil {
		t.Fatal("cancelled job has no finished timestamp")
	}
	if _, err := service.EnqueueContext(context.Background(), models.ConversionRequest{}); !errors.Is(err, ErrConversionServiceClosed) {
		t.Fatalf("enqueue after shutdown error = %v, want ErrConversionServiceClosed", err)
	}
}

func TestConversionEnqueueContextDoesNotDropOnSaturation(t *testing.T) {
	runner := &blockingConversionRunner{started: make(chan struct{})}
	service := NewConversionService(&FFmpegService{})
	service.ffmpeg = runner
	if _, err := service.EnqueueContext(context.Background(), models.ConversionRequest{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("conversion worker did not start")
	}
	for i := 0; i < cap(service.queue); i++ {
		if _, err := service.EnqueueContext(context.Background(), models.ConversionRequest{}); err != nil {
			t.Fatalf("fill enqueue %d failed: %v", i, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if job, err := service.EnqueueContext(ctx, models.ConversionRequest{}); job != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("saturated enqueue = (%v, %v), want nil and deadline exceeded", job, err)
	}
	if got := len(service.All()); got != cap(service.queue)+1 {
		t.Fatalf("jobs after rejected enqueue = %d, want %d", got, cap(service.queue)+1)
	}
	service.Shutdown()
}
