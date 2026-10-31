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
	StatePaused      DownloadState = "paused"
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

// AudioSelectionMode controls which audio streams are included in a
// video-plus-audio download. The zero value is the default: one best audio
// stream. The legacy MultiAudio and SelectedAudioTrackIDs fields are honored
// when AudioSelectionMode is not set.
type AudioSelectionMode string

const (
	AudioSelectionNone   AudioSelectionMode = "none"
	AudioSelectionAll    AudioSelectionMode = "all"
	AudioSelectionCustom AudioSelectionMode = "custom"

	// AudioSelectionDefault is an explicit spelling of the zero-value behavior.
	AudioSelectionDefault AudioSelectionMode = "default"

	// Long-form aliases make the state names discoverable alongside the type.
	AudioSelectionModeNone   = AudioSelectionNone
	AudioSelectionModeAll    = AudioSelectionAll
	AudioSelectionModeCustom = AudioSelectionCustom
)

// SubtitleSelectionMode controls subtitle download and language selection.
// The zero value preserves the legacy WriteSubtitles/SubtitleLangs behavior.
type SubtitleSelectionMode string

const (
	SubtitleSelectionNone   SubtitleSelectionMode = "none"
	SubtitleSelectionAll    SubtitleSelectionMode = "all"
	SubtitleSelectionCustom SubtitleSelectionMode = "custom"

	// SubtitleSelectionDefault is an explicit spelling of the legacy behavior.
	SubtitleSelectionDefault SubtitleSelectionMode = "default"

	// Long-form aliases make the state names discoverable alongside the type.
	SubtitleSelectionModeNone   = SubtitleSelectionNone
	SubtitleSelectionModeAll    = SubtitleSelectionAll
	SubtitleSelectionModeCustom = SubtitleSelectionCustom
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
	Is360     bool // prefer an equirectangular stream for spherical video

	// Audio options
	AudioFormat      string // mp3, m4a, aac, flac, opus, wav
	AudioBitrateKbps int    // 0 = best

	// Explicit format IDs (override presets)
	VideoFormatID string
	AudioFormatID string

	// Extras
	WriteSubtitles        bool // legacy switch; use SubtitleSelectionMode for new callers
	SubtitleLangs         []string
	SubtitleSelectionMode SubtitleSelectionMode
	SubtitleSelection     SubtitleSelectionMode // concise alias for new callers
	MultiAudio            bool                  // legacy switch; use AudioSelectionMode for new callers
	SelectedAudioTrackIDs []string
	AudioSelectionMode    AudioSelectionMode
	AudioSelection        AudioSelectionMode // concise alias for new callers
	EmbedThumbnail        bool
	EmbedMetadata         bool
	PreferVP9             bool
	NoCookies             bool // do not attach configured browser cookies to this request

	// Playlist context
	PlaylistTitle string
	PlaylistIndex int

	// UI-only
	PlaylistBatchID           uint64
	ThumbnailURL              string
	ExpectedSizeBytes         int64
	ExpectedSizeIsApproximate bool
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
	ID          string
	Request     DownloadRequest
	State       DownloadState
	Progress    ProgressInfo
	OutputPath  string
	Error       string
	ErrorDetail string
	CreatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time

	cancelCh        chan struct{}
	pauseRequested  bool
	stagingDir      string
	queueGeneration uint64
	mu              sync.Mutex
}

// DownloadTaskSnapshot is an immutable view of a task suitable for UI and
// persistence code. Services mutate DownloadTask from worker goroutines, so
// callers must not read its exported fields while work is in progress.
type DownloadTaskSnapshot struct {
	ID          string
	Request     DownloadRequest
	State       DownloadState
	Progress    ProgressInfo
	OutputPath  string
	StagingDir  string
	Error       string
	ErrorDetail string
	CreatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
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

// RestorePausedDownloadTask recreates a persisted task without enqueueing it.
func RestorePausedDownloadTask(snapshot DownloadTaskSnapshot) *DownloadTask {
	return &DownloadTask{
		ID:          snapshot.ID,
		Request:     cloneDownloadRequest(snapshot.Request),
		State:       StatePaused,
		Progress:    snapshot.Progress,
		OutputPath:  snapshot.OutputPath,
		Error:       snapshot.Error,
		ErrorDetail: snapshot.ErrorDetail,
		CreatedAt:   snapshot.CreatedAt,
		StartedAt:   cloneTime(snapshot.StartedAt),
		FinishedAt:  nil,
		cancelCh:    make(chan struct{}),
		stagingDir:  snapshot.StagingDir,
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
	t.pauseRequested = false
	select {
	case <-t.cancelCh:
	default:
		close(t.cancelCh)
	}
}

// RequestPause stops the current process while preserving the task as
// resumable. The manager changes the visible state after the process exits.
func (t *DownloadTask) RequestPause() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.State.IsTerminal() {
		return
	}
	t.pauseRequested = true
	select {
	case <-t.cancelCh:
	default:
		close(t.cancelCh)
	}
}

func (t *DownloadTask) PauseRequested() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pauseRequested
}

func (t *DownloadTask) Cancelled() bool {
	t.mu.Lock()
	ch := t.cancelCh
	t.mu.Unlock()
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Snapshot returns a consistent copy of the task's mutable state.
func (t *DownloadTask) Snapshot() DownloadTaskSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return DownloadTaskSnapshot{
		ID:          t.ID,
		Request:     cloneDownloadRequest(t.Request),
		State:       t.State,
		Progress:    t.Progress,
		OutputPath:  t.OutputPath,
		StagingDir:  t.stagingDir,
		Error:       t.Error,
		ErrorDetail: t.ErrorDetail,
		CreatedAt:   t.CreatedAt,
		StartedAt:   cloneTime(t.StartedAt),
		FinishedAt:  cloneTime(t.FinishedAt),
	}
}

func (t *DownloadTask) StateValue() DownloadState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.State
}

func (t *DownloadTask) SetError(message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Error = message
}

func (t *DownloadTask) SetErrorDetail(message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ErrorDetail = message
}

func (t *DownloadTask) SetOutputPath(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.OutputPath = path
}

func (t *DownloadTask) SetStagingDir(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stagingDir = path
}

func (t *DownloadTask) StagingDir() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stagingDir
}

func (t *DownloadTask) ClearStagingDir() {
	t.SetStagingDir("")
}

// Done is closed when Cancel is called; safe for select across packages.
func (t *DownloadTask) Done() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cancelCh
}

// ResetForRetry prepares a terminal task to be queued again.
func (t *DownloadTask) ResetForRetry() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancelCh = make(chan struct{})
	t.queueGeneration++
	t.pauseRequested = false
	t.Error = ""
	t.ErrorDetail = ""
	t.Progress = ProgressInfo{}
	t.OutputPath = ""
	t.StartedAt = nil
	t.FinishedAt = nil
	t.State = StatePending
}

// ResetForResume requeues a paused task without clearing its progress or
// staging directory. yt-dlp can then continue its existing .part files.
func (t *DownloadTask) ResetForResume() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancelCh = make(chan struct{})
	t.queueGeneration++
	t.pauseRequested = false
	t.Error = ""
	t.ErrorDetail = ""
	t.FinishedAt = nil
	t.State = StatePending
}

// QueueGeneration identifies the current enqueue attempt. A paused or retried
// task can leave an older item in the bounded channel; workers discard that
// stale item instead of running the task twice.
func (t *DownloadTask) QueueGeneration() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.queueGeneration
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
	snapshot := t.Snapshot()
	if snapshot.OutputPath != "" {
		return filepath.Base(snapshot.OutputPath)
	}
	return snapshot.Request.Title
}

func cloneDownloadRequest(req DownloadRequest) DownloadRequest {
	req.SubtitleLangs = append([]string(nil), req.SubtitleLangs...)
	req.SelectedAudioTrackIDs = append([]string(nil), req.SelectedAudioTrackIDs...)
	return req
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
