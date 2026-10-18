package models

import (
	"sync"
	"testing"
)

func TestConversionJobCancelIsIdempotentConcurrently(t *testing.T) {
	job := NewConversionJob(ConversionRequest{Source: "input.mp4", TargetFormat: "mp3"})

	const callers = 64
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			job.Cancel()
		}()
	}
	wg.Wait()

	if !job.Cancelled() {
		t.Fatal("job should be cancelled after concurrent Cancel calls")
	}
	select {
	case <-job.Done():
	default:
		t.Fatal("Done channel should be closed after cancellation")
	}
}

func TestConversionJobFinalizationSkipsCancelledJob(t *testing.T) {
	job := NewConversionJob(ConversionRequest{})
	job.Cancel()
	called := false
	if job.FinalizeIfNotCancelled(func() { called = true }) {
		t.Fatal("cancelled job should not finalize")
	}
	if called {
		t.Fatal("finalizer ran for a cancelled job")
	}
}
