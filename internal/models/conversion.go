package models

import "time"

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
	ConvPending    ConversionState = "pending"
	ConvRunning    ConversionState = "running"
	ConvCompleted  ConversionState = "completed"
	ConvError      ConversionState = "error"
	ConvCancelled  ConversionState = "cancelled"
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

// Done is closed when Cancel is called; safe for select across packages.
func (j *ConversionJob) Done() <-chan struct{} {
	return j.cancelCh
}
