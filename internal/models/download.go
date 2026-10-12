package models

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// DownloadState mirrors the original Python state machine.
type DownloadState string

const (
	StatePending     DownloadState = "pending"
	StatePreparing   DownloadState = "preparing"
	StateDownloading DownloadState = "downloading"
	StateProcessing  DownloadState = "processing"
	StateCompleted   DownloadState = "completed"
	StateError       DownloadState = "error"
	StateCancelled   DownloadState = "cancelled"
)

func (s DownloadState) IsTerminal() bool {
	return s == StateCompleted || s == StateError || s == StateCancelled
}

// DownloadMode is the high-level download intent.
type DownloadMode string

const (
	ModeVideoOnly  DownloadMode = "video"
	ModeAudioOnly  DownloadMode = "audio"
	ModeVideoAudio DownloadMode = "video_audio"
)

// DownloadRequest holds everything needed for one video download.
type DownloadRequest struct {
	URL       string
	Title     string
	Mode      DownloadMode
	OutputDir string

	// Video options
	Container string // mp4, mkv, webm …
	MaxHeight int    // 0 = best
	MaxFPS    int

	// Audio options
	AudioFormat      string // mp3, m4a, aac, flac, opus, wav
	AudioBitrateKbps int    // 0 = best

	// Explicit format IDs (override presets)
	VideoFormatID string
	AudioFormatID string

	// Extras
	WriteSubtitles        bool
	SubtitleLangs         []string
	MultiAudio            bool
	SelectedAudioTrackIDs []string
	EmbedThumbnail        bool
	EmbedMetadata         bool
	PreferVP9             bool

	// Playlist context
	PlaylistTitle string
	PlaylistIndex int

	// UI-only
	ThumbnailURL string
}

// ProgressInfo is a snapshot of download progress.
type ProgressInfo struct {
	DownloadedBytes int64
	TotalBytes      int64
	TotalIsEstimate bool
	SpeedBps        float64
	ETA             time.Duration
	FragmentIndex   int
	FragmentCount   int
	Percent         float64 // 0–100
	Status          string
}

var idCounter atomic.Uint64

// DownloadTask is one unit of work in the queue.
type DownloadTask struct {
	ID         string
	Request    DownloadRequest
	State      DownloadState
	Progress   ProgressInfo
	OutputPath string
	Error      string
	ErrorDetail string
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time

	cancelCh chan struct{}
	mu       sync.Mutex
}

func NewDownloadTask(req DownloadRequest) *DownloadTask {
	n := idCounter.Add(1)
	return &DownloadTask{
		ID:        time.Now().Format("20060102-150405") + "-" + itoa(int(n)),
		Request:   req,
		State:     StatePending,
		CreatedAt: time.Now(),
		cancelCh:  make(chan struct{}),
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func (t *DownloadTask) Cancel() {
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case <-t.cancelCh:
	default:
		close(t.cancelCh)
	}
}

func (t *DownloadTask) Cancelled() bool {
	select {
	case <-t.cancelCh:
		return true
	default:
		return false
	}
}

// Done is closed when Cancel is called; safe for select across packages.
func (t *DownloadTask) Done() <-chan struct{} {
	return t.cancelCh
}

// ResetForRetry prepares a terminal task to be queued again.
func (t *DownloadTask) ResetForRetry() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancelCh = make(chan struct{})
	t.Error = ""
	t.ErrorDetail = ""
	t.Progress = ProgressInfo{}
	t.OutputPath = ""
	t.FinishedAt = nil
	t.State = StatePending
}

func (t *DownloadTask) SetState(s DownloadState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.State = s
	now := time.Now()
	if s == StatePreparing || s == StateDownloading {
		if t.StartedAt == nil {
			t.StartedAt = &now
		}
	}
	if s.IsTerminal() {
		t.FinishedAt = &now
	}
}

func (t *DownloadTask) SetProgress(p ProgressInfo) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Progress = p
}

func (t *DownloadTask) Filename() string {
	if t.OutputPath != "" {
		return filepath.Base(t.OutputPath)
	}
	return t.Request.Title
}
