package models

import (
	"sync"
	"time"
)

type ConversionMode string

const (
	ConversionRemux    ConversionMode = "remux"
	ConversionReencode ConversionMode = "reencode"
)

type MediaKind string

const (
	KindVideo MediaKind = "video"
	KindAudio MediaKind = "audio"
)

type ConversionRequest struct {
	Source           string
	TargetFormat     string // mp4, mkv, webm, mp3, m4a, …
	Kind             MediaKind
	Mode             ConversionMode
	AudioBitrateKbps int
	KeepOriginal     bool
}

type ConversionState string

const (
	ConvPending   ConversionState = "pending"
	ConvRunning   ConversionState = "running"
	ConvCompleted ConversionState = "completed"
	ConvError     ConversionState = "error"
	ConvCancelled ConversionState = "cancelled"
)

func (s ConversionState) IsTerminal() bool {
	return s == ConvCompleted || s == ConvError || s == ConvCancelled
}

type ConversionJob struct {
	ID         string
	Request    ConversionRequest
	State      ConversionState
	Percent    float64 // 0–100
	OutputPath string
	Error      string
	CreatedAt  time.Time
	FinishedAt *time.Time

	cancelCh chan struct{}
	mu       sync.Mutex
}

// ConversionJobSnapshot is an immutable view for UI and persistence code.
type ConversionJobSnapshot struct {
	ID         string
	Request    ConversionRequest
	State      ConversionState
	Percent    float64
	OutputPath string
	Error      string
	CreatedAt  time.Time
	FinishedAt *time.Time
}

func NewConversionJob(req ConversionRequest) *ConversionJob {
	n := idCounter.Add(1)
	return &ConversionJob{
		ID:        time.Now().Format("20060102-150405") + "-c" + itoa(int(n)),
		Request:   req,
		State:     ConvPending,
		CreatedAt: time.Now(),
		cancelCh:  make(chan struct{}),
	}
}

func (j *ConversionJob) Cancel() {
	j.mu.Lock()
	defer j.mu.Unlock()
	select {
	case <-j.cancelCh:
	default:
		close(j.cancelCh)
	}
}

func (j *ConversionJob) Cancelled() bool {
	select {
	case <-j.cancelCh:
		return true
	default:
		return false
	}
}

func (j *ConversionJob) Snapshot() ConversionJobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	return ConversionJobSnapshot{
		ID:         j.ID,
		Request:    j.Request,
		State:      j.State,
		Percent:    j.Percent,
		OutputPath: j.OutputPath,
		Error:      j.Error,
		CreatedAt:  j.CreatedAt,
		FinishedAt: cloneConversionTime(j.FinishedAt),
	}
}

func (j *ConversionJob) StateValue() ConversionState {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.State
}

func (j *ConversionJob) SetState(state ConversionState) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.State = state
}

func (j *ConversionJob) SetProgress(percent float64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Percent = percent
}

func (j *ConversionJob) SetError(message string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Error = message
}

func (j *ConversionJob) SetOutput(path string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.OutputPath = path
}

func (j *ConversionJob) SetFinishedAt(value time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	copy := value
	j.FinishedAt = &copy
}

// Done is closed when Cancel is called; safe for select across packages.
func (j *ConversionJob) Done() <-chan struct{} {
	return j.cancelCh
}

// FinalizeIfNotCancelled serializes a final destructive step with Cancel.
// The callback should be short; it runs while the job state is locked so a
// cancellation that wins the race prevents finalization.
func (j *ConversionJob) FinalizeIfNotCancelled(finalize func()) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	select {
	case <-j.cancelCh:
		return false
	default:
		finalize()
		return true
	}
}

func cloneConversionTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
