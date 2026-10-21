package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/config"
	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
	"github.com/fazi-gondal/video-downloader-mygo/internal/tools"
)

// YtDlpService wraps the yt-dlp CLI. All methods are blocking and must
// be called from worker goroutines (never the UI thread).
type YtDlpService struct {
	ffmpegPath string
	ytdlpPath  string
	settings   func() config.AppSettings
}

const (
	maxYtDlpDiagnosticBytes = 1 << 20
	progressEmitInterval    = 250 * time.Millisecond
)

type boundedDiagnosticBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedDiagnosticBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) >= b.limit {
		b.buf.Reset()
		_, _ = b.buf.Write(p[len(p)-b.limit:])
		b.truncated = true
		return len(p), nil
	}
	if b.buf.Len()+len(p) > b.limit {
		combined := make([]byte, 0, b.buf.Len()+len(p))
		combined = append(combined, b.buf.Bytes()...)
		combined = append(combined, p...)
		combined = combined[len(combined)-b.limit:]
		b.buf.Reset()
		_, _ = b.buf.Write(combined)
		b.truncated = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *boundedDiagnosticBuffer) String() string {
	text := b.buf.String()
	if b.truncated {
		text += "\n[yt-dlp diagnostics truncated]"
	}
	return text
}

func NewYtDlpService(ffmpegPath string, settings func() config.AppSettings) *YtDlpService {
	yt := tools.YtDlp()
	if yt == "" {
		yt = "yt-dlp" // hope PATH at exec time
	}
	if ffmpegPath == "" {
		ffmpegPath = tools.FFmpeg()
	}
	return &YtDlpService{
		ffmpegPath: ffmpegPath,
		ytdlpPath:  yt,
		settings:   settings,
	}
}

func (s *YtDlpService) Bin() string { return s.ytdlpPath }

// Analyze performs a fast flat extraction (good for playlists).
func (s *YtDlpService) Analyze(ctx context.Context, url string) (any, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil, fmt.Errorf("empty URL")
	}
	if IsAryPlusURL(url) {
		return s.analyzeAryPlus(ctx, url)
	}
	args := s.baseArgs()
	args = append(args,
		"--dump-single-json",
		"--flat-playlist",
		"--no-download",
		"--no-warnings",
		url,
	)
	out, err := s.run(ctx, args)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse yt-dlp json: %w", err)
	}
	if t, _ := raw["_type"].(string); t == "playlist" {
		return parsePlaylist(raw, url), nil
	}
	return parseMedia(raw, url), nil
}

// FetchFormats does a full extraction of a single video (formats + tracks).
func (s *YtDlpService) FetchFormats(ctx context.Context, url string) (*models.MediaInfo, error) {
	url = strings.TrimSpace(url)
	if IsAryPlusURL(url) {
		return s.fetchFormatsAryPlus(ctx, url)
	}
	args := s.fetchFormatsArgs(url)
	out, err := s.run(ctx, args)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	return parseMedia(raw, url), nil
}

func (s *YtDlpService) analyzeAryPlus(ctx context.Context, rawURL string) (any, error) {
	if IsAryPlusSeriesURL(rawURL) {
		return FetchAryPlusSeriesPlaylist(ctx, rawURL)
	}
	return s.fetchFormatsAryPlus(ctx, rawURL)
}

func (s *YtDlpService) fetchFormatsAryPlus(ctx context.Context, rawURL string) (*models.MediaInfo, error) {
	ep, err := ResolveAryPlusEpisode(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	if ep.PlatformID == "1" {
		return s.FetchFormats(ctx, "https://www.youtube.com/watch?v="+ep.VideoID)
	}
	if ep.PlatformID == "2" {
		return s.FetchFormats(ctx, "https://www.dailymotion.com/video/"+ep.VideoID)
	}

	media, err := s.FetchFormats(ctx, ep.VideoSource)
	if err != nil {
		return nil, err
	}
	media.ID = ep.VideoID
	media.Title = ep.Title
	if ep.Description != "" {
		media.Description = ep.Description
	}
	if ep.Thumbnail != "" {
		media.Thumbnail = ep.Thumbnail
	}
	if ep.Duration > 0 {
		media.Duration = ep.Duration
	}
	media.OriginalURL = rawURL
	media.WebpageURL = rawURL
	media.Extractor = "aryplus"
	return media, nil
}

func (s *YtDlpService) fetchFormatsArgs(url string) []string {
	args := s.baseArgs()
	return append(args,
		"--dump-single-json",
		"--no-download",
		"--no-playlist",
		"--no-warnings",
		url,
	)
}

// Download runs yt-dlp for a task and streams progress via the callback.
// Returns the final output path.
func (s *YtDlpService) Download(ctx context.Context, task *models.DownloadTask, onProgress func(models.ProgressInfo)) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req := task.Request
	if IsAryPlusEpisodeURL(req.URL) {
		if ep, err := ResolveAryPlusEpisode(ctx, req.URL); err == nil {
			if ep.PlatformID == "1" {
				req.URL = "https://www.youtube.com/watch?v=" + ep.VideoID
			} else if ep.PlatformID == "2" {
				req.URL = "https://www.dailymotion.com/video/" + ep.VideoID
			} else if ep.VideoSource != "" {
				req.URL = ep.VideoSource
				if req.Title == "" {
					req.Title = ep.Title
				}
			}
		}
	}
	stagingDir := task.StagingDir()
	var err error
	if stagingDir == "" {
		stagingDir, err = newDownloadStagingDir(req.OutputDir)
		if err != nil {
			return "", err
		}
		task.SetStagingDir(stagingDir)
	} else if err := os.MkdirAll(stagingDir, 0o750); err != nil {
		return "", fmt.Errorf("recreate download staging directory: %w", err)
	}
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			_ = os.RemoveAll(stagingDir)
			task.ClearStagingDir()
		}
	}()
	req.OutputDir = stagingDir
	args := s.downloadArgs(req)
	settings := s.appSettings()
	subtitleErrorsIgnored := subtitleDownloadEnabled(req, settings)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, s.ytdlpPath, args...) // #nosec G204 -- exec.CommandContext passes argv directly without a shell.
	prepareBackgroundCmd(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start yt-dlp: %w", err)
	}

	// task cancellation is independent from the caller context, so bridge it
	// into the command context and always reap the process below.
	taskWatchDone := make(chan struct{})
	taskWatchExited := make(chan struct{})
	go func() {
		defer close(taskWatchExited)
		select {
		case <-task.Done():
			cancel()
			_ = cmd.Process.Kill()
		case <-taskWatchDone:
		}
	}()

	stdoutDone := make(chan struct {
		path string
		err  error
	}, 1)
	go func() {
		var lastPath string
		var lastProgressAt time.Time
		var lastProgressStatus string
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if p, ok := parseProgressLine(line); ok && onProgress != nil {
				now := time.Now()
				statusChanged := p.Status != "" && p.Status != lastProgressStatus
				if lastProgressAt.IsZero() || statusChanged || now.Sub(lastProgressAt) >= progressEmitInterval {
					onProgress(p)
					lastProgressAt = now
					lastProgressStatus = p.Status
				}
			}
			// yt-dlp prints the final path on some templates; capture Destination.
			if strings.Contains(line, "[download] Destination:") {
				lastPath = strings.TrimSpace(strings.SplitN(line, "Destination:", 2)[1])
			}
			if strings.Contains(line, "Merging formats into") {
				if i := strings.Index(line, "\""); i >= 0 {
					if j := strings.LastIndex(line, "\""); j > i {
						lastPath = line[i+1 : j]
					}
				}
			}
			// after_move:filepath is emitted after subtitle embedding/remuxing and
			// therefore points at the final playable file.
			if info, statErr := os.Stat(line); line != "" && statErr == nil && !info.IsDir() {
				lastPath = line
			}
		}
		scanErr := scanner.Err()
		if scanErr != nil {
			cancel()
		}
		stdoutDone <- struct {
			path string
			err  error
		}{lastPath, scanErr}
	}()

	stderrDone := make(chan struct {
		text string
		err  error
	}, 1)
	go func() {
		diagnostics := &boundedDiagnosticBuffer{limit: maxYtDlpDiagnosticBytes}
		_, readErr := io.Copy(diagnostics, stderr)
		stderrDone <- struct {
			text string
			err  error
		}{diagnostics.String(), readErr}
	}()

	waitErr := cmd.Wait()
	stdoutResult := <-stdoutDone
	stderrResult := <-stderrDone
	close(taskWatchDone)
	cancel()
	<-taskWatchExited

	if task.PauseRequested() {
		cleanupStaging = false
		return "", context.Canceled
	}
	if task.Cancelled() {
		return "", context.Canceled
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if stdoutResult.err != nil {
		return "", fmt.Errorf("read yt-dlp output: %w", stdoutResult.err)
	}
	if stderrResult.err != nil && waitErr == nil {
		return "", fmt.Errorf("read yt-dlp diagnostics: %w", stderrResult.err)
	}
	lastPath := stdoutResult.path
	stderrText := stderrResult.text
	if waitErr != nil {
		if !subtitleErrorsIgnored || !ytDlpOnlySubtitleErrors(stderrText) {
			return "", fmt.Errorf("%s", ytDlpErrorMessage(stderrText, waitErr))
		}
	}
	if subtitleErrorsIgnored {
		if nonSubtitleErrors := ytDlpNonSubtitleErrorLines(stderrText); len(nonSubtitleErrors) > 0 {
			return "", fmt.Errorf("%s", ytDlpErrorMessage(stderrText, waitErr))
		}
		task.SetErrorDetail(ytDlpSubtitleWarning(stderrText))
	}

	if stagingDir != "" {
		// The command runs in an isolated directory, so a fallback can only
		// select files created by this request. This avoids associating an
		// unrelated or concurrently downloaded file from the shared output dir.
		if _, inStaging := relativeStagingPath(stagingDir, lastPath); !inStaging || !isRegularFile(lastPath) {
			lastPath = newestFile(stagingDir)
		}
		lastPath, err = promoteStagedDownload(stagingDir, task.Request.OutputDir, lastPath)
		if err != nil {
			return "", err
		}
	}
	return lastPath, nil
}

// DiscardPartial removes a paused task's staging directory when the user
// chooses permanent cancellation instead of resuming it.
func (s *YtDlpService) DiscardPartial(task *models.DownloadTask) {
	if task == nil {
		return
	}
	if stagingDir := task.StagingDir(); stagingDir != "" {
		_ = os.RemoveAll(stagingDir)
		task.ClearStagingDir()
	}
}

func newDownloadStagingDir(outputDir string) (string, error) {
	if outputDir == "" {
		outputDir = "."
	}
	if err := os.MkdirAll(outputDir, 0o750); err != nil {
		return "", fmt.Errorf("create download output directory: %w", err)
	}
	stagingDir, err := os.MkdirTemp(outputDir, ".mygo-download-*")
	if err != nil {
		return "", fmt.Errorf("create download staging directory: %w", err)
	}
	return stagingDir, nil
}

func promoteStagedDownload(stagingDir, outputDir, selectedPath string) (string, error) {
	selectedPath = filepath.Clean(selectedPath)
	selectedRel, selectedInStaging := relativeStagingPath(stagingDir, selectedPath)
	if selectedPath == "." || !selectedInStaging {
		selectedRel = ""
	}

	var promotedPath string
	err := filepath.Walk(stagingDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(stagingDir, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(outputDir, rel)
		if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
			return err
		}
		if err := moveStagedFile(path, destination); err != nil {
			return err
		}
		if selectedRel != "" && rel == selectedRel {
			promotedPath = destination
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("finalize yt-dlp output: %w", err)
	}
	return promotedPath, nil
}

func relativeStagingPath(stagingDir, path string) (string, bool) {
	if path == "" {
		return "", false
	}
	rel, err := filepath.Rel(stagingDir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

func moveStagedFile(source, destination string) error {
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return err
	}
	if info, err := os.Stat(destination); err == nil {
		if info.IsDir() {
			return fmt.Errorf("destination %q is a directory", destination)
		}
		// yt-dlp normally skips an existing output. Preserve that behavior
		// while removing the duplicate staged file.
		return os.Remove(source)
	} else if !os.IsNotExist(err) {
		return err
	}

	// A hard link plus removal is an atomic no-overwrite move on the same
	// filesystem, which is where the staging directory is created. This also
	// prevents two concurrent promotions from replacing each other's output.
	if err := os.Link(source, destination); err == nil {
		return os.Remove(source)
	} else if _, statErr := os.Stat(destination); statErr == nil {
		return os.Remove(source)
	}

	// Fall back for filesystems without hard-link support while retaining
	// exclusive creation semantics.
	sourceFile, err := os.Open(source) // #nosec G304 -- source is a file discovered under the request-specific staging directory.
	if err != nil {
		return err
	}
	destinationFile, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, sourceInfo.Mode().Perm()) // #nosec G304 -- destination is derived from a validated staging-relative path.
	if err != nil {
		_ = sourceFile.Close()
		if _, statErr := os.Stat(destination); statErr == nil {
			return os.Remove(source)
		}
		return err
	}
	_, copyErr := io.Copy(destinationFile, sourceFile)
	sourceCloseErr := sourceFile.Close()
	destinationCloseErr := destinationFile.Close()
	if copyErr != nil {
		_ = os.Remove(destination)
		return copyErr
	}
	if sourceCloseErr != nil {
		return sourceCloseErr
	}
	if destinationCloseErr != nil {
		return destinationCloseErr
	}
	return os.Remove(source)
}

func isRegularFile(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (s *YtDlpService) downloadArgs(req models.DownloadRequest) []string {
	args := s.buildDownloadArgs(req)
	// Progress template that we can parse. after_move:filepath is emitted only
	// after subtitle embedding/remuxing, so it identifies the final playable
	// output rather than a temporary media or subtitle sidecar.
	args = append(args,
		"--continue",
		"--part",
		"--newline",
		"--progress",
		// The `download:` prefix selects yt-dlp's progress template type; it is
		// not printed. Include a literal marker so stdout can be distinguished
		// from diagnostics, paths, and other yt-dlp output.
		"--progress-template", "download:PROGRESS|%(progress.status)s|%(progress.downloaded_bytes)s|%(progress.total_bytes)s|%(progress.total_bytes_estimate)s|%(progress.speed)s|%(progress.eta)s|%(progress.fragment_index)s|%(progress.fragment_count)s",
		"--print", "after_move:filepath",
		req.URL,
	)
	return args
}

func subtitleDownloadEnabled(req models.DownloadRequest, settings config.AppSettings) bool {
	if req.Mode == models.ModeAudioOnly {
		return false
	}
	selection, _ := subtitleSelection(req, settings)
	return selection != models.SubtitleSelectionNone
}

func (s *YtDlpService) baseArgs() []string {
	st := s.appSettings()
	args := []string{
		"--prefer-free-formats",
		// Keep the normal client for broad format coverage, but add the embedded
		// client fallback and exclude Android VR. This avoids stopping on the
		// VisionOS cookie/bot check and preserves YouTube's equirectangular HLS
		// streams for 360-degree videos.
		"--extractor-args", "youtube:player_client=default,web_embedded,-android_vr",
		// The embedded client works for the public formats used here without
		// requiring an external JavaScript runtime on the user's machine.
		"--no-js-runtimes",
	}
	if s.ffmpegPath != "" {
		args = append(args, "--ffmpeg-location", s.ffmpegPath)
	}
	if st.Proxy != "" {
		args = append(args, "--proxy", st.Proxy)
	}
	if st.CookiesBrowser != "" {
		args = append(args, "--cookies-from-browser", st.CookiesBrowser)
	}
	keys := make([]string, 0, len(st.CustomHeaders))
	for key := range st.CustomHeaders {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if value := strings.TrimSpace(st.CustomHeaders[key]); key != "" && value != "" {
			args = append(args, "--add-header", key+":"+value)
		}
	}
	if st.RateLimitKbps > 0 {
		args = append(args, "--limit-rate", fmt.Sprintf("%dK", st.RateLimitKbps))
	}
	if st.ConcurrentFragments > 1 {
		args = append(args, "--concurrent-fragments", strconv.Itoa(st.ConcurrentFragments))
	}
	return args
}

func (s *YtDlpService) buildDownloadArgs(req models.DownloadRequest) []string {
	args := s.baseArgs()
	st := s.appSettings()
	subtitleMode, subtitleLangs := models.SubtitleSelectionNone, []string(nil)
	if req.Mode != models.ModeAudioOnly {
		subtitleMode, subtitleLangs = subtitleSelection(req, st)
	}
	embedSubtitles := subtitleMode != models.SubtitleSelectionNone

	_ = os.MkdirAll(req.OutputDir, 0o750)
	outTpl := filepath.Join(req.OutputDir, "%(title).200B [%(id)s].%(ext)s")
	if req.PlaylistTitle != "" {
		outTpl = filepath.Join(req.OutputDir, "%(playlist_title).100B", "%(playlist_index)02d - %(title).150B [%(id)s].%(ext)s")
	}
	if req.Title != "" && isDirectStreamURL(req.URL) {
		cleanTitle := sanitizeFilename(req.Title)
		if req.PlaylistTitle != "" {
			cleanPlaylist := sanitizeFilename(req.PlaylistTitle)
			outTpl = filepath.Join(req.OutputDir, cleanPlaylist, cleanTitle+".%(ext)s")
		} else {
			outTpl = filepath.Join(req.OutputDir, cleanTitle+".%(ext)s")
		}
	}
	args = append(args, "-o", outTpl)

	// Format selection
	switch req.Mode {
	case models.ModeAudioOnly:
		args = append(args, "-f", audioFormatSelector(req), "-x", "--audio-format", req.AudioFormat)
		if req.AudioBitrateKbps > 0 {
			args = append(args, "--audio-quality", strconv.Itoa(req.AudioBitrateKbps)+"K")
		}
	case models.ModeVideoOnly:
		args = append(args, "-f", videoFormatSelector(req))
		if embedSubtitles {
			args = append(args, "--merge-output-format", "mkv", "--remux-video", "mkv")
		}
	default: // VIDEO_AUDIO
		args = append(args, "-f", videoAudioSelector(req))
		merge := req.Container
		if merge == "" {
			merge = "mp4"
		}
		// MKV is the reliable VLC target for text subtitles and also supports
		// the multiple audio streams requested by the multi-audio selector.
		multiAudio := requiresMultiAudio(req)
		if multiAudio || embedSubtitles {
			merge = "mkv"
		} else if req.PreferVP9 || strings.Contains(strings.ToLower(req.AudioFormatID), "opus") {
			if merge == "mp4" {
				merge = "mkv"
			}
		}
		if multiAudio {
			args = append(args, "--audio-multistreams")
		}
		args = append(args, "--merge-output-format", merge)
		if embedSubtitles {
			args = append(args, "--remux-video", "mkv")
		}
	}
	if req.PreferVP9 && req.Mode != models.ModeAudioOnly && req.VideoFormatID == "" {
		// Sorting is preferable to filtering: it keeps VP9 as the first choice
		// while retaining a working fallback when a video has no VP9 stream.
		args = append(args, "--format-sort", "vcodec:vp9,res,fps,acodec:opus,acodec:m4a")
	}

	if req.Mode != models.ModeAudioOnly {
		if subtitleMode != models.SubtitleSelectionNone {
			// Subtitle URLs are requested separately from media streams and are
			// commonly rate-limited more aggressively. Space requests and back
			// off between HTTP retries instead of immediately retrying a 429.
			// yt-dlp otherwise keeps the downloaded sidecars when --write-subs
			// and --embed-subs are used together.
			args = append(args,
				"--write-subs", "--embed-subs",
				"--convert-subs", "srt",
				"--compat-options", "no-keep-subs",
				"--sleep-subtitles", "5",
				"--retry-sleep", "http:exp=5:60",
				// Subtitle servers can reject one language while the selected
				// media streams remain downloadable. The service validates stderr
				// after yt-dlp exits so this does not mask media failures.
				"--ignore-errors",
			)
			if subtitleMode == models.SubtitleSelectionAll {
				args = append(args, "--sub-langs", "all,-live_chat")
			} else if len(subtitleLangs) > 0 {
				args = append(args, "--sub-langs", strings.Join(subtitleLangs, ","))
			}
		}
	}
	if req.EmbedThumbnail || st.EmbedThumbnail {
		args = append(args, "--embed-thumbnail")
	}
	if req.EmbedMetadata || st.EmbedMetadata {
		args = append(args, "--embed-metadata", "--embed-chapters")
	}
	return args
}

func (s *YtDlpService) appSettings() config.AppSettings {
	if s.settings == nil {
		return config.AppSettings{}
	}
	return s.settings()
}

func audioFormatSelector(req models.DownloadRequest) string {
	if req.AudioFormatID != "" {
		return req.AudioFormatID
	}
	return "ba/b"
}

func videoFormatSelector(req models.DownloadRequest) string {
	if req.VideoFormatID != "" {
		return req.VideoFormatID
	}
	filters := videoFilters(req)
	sel := preferredVideoSelector(req, "bv", filters)
	if filters != "" {
		return sel + "/bv/b" + filters + "/b"
	}
	return sel + "/bv/b"
}

// bestAudioSelector prefers Opus at the highest available bitrate, then any bestaudio.
func bestAudioSelector(req models.DownloadRequest) string {
	if selector := audioFormatSelector(req); selector != "ba/b" {
		return selector
	}
	return "ba/b"
}

func videoAudioSelector(req models.DownloadRequest) string {
	video := videoAudioVideoSelector(req)
	selectionMode, selected := audioSelection(req)
	if req.VideoFormatID != "" && req.AudioFormatID != "" && selectionMode == models.AudioSelectionNone {
		return req.VideoFormatID + "+" + req.AudioFormatID
	}
	if selectionMode == models.AudioSelectionCustom && len(selected) > 0 {
		audioParts := make([]string, 0, len(selected))
		for _, track := range selected {
			audioParts = append(audioParts, audioSelector(track))
		}
		audio := strings.Join(audioParts, "+")
		if len(selected) == 1 {
			return video + "+" + audio + "/" + video + "+ba/b"
		}
		return video + "+" + audio + "/" + video + "+mergeall[vcodec=none]/" + video + "+ba/b"
	}

	if selectionMode == models.AudioSelectionAll {
		filters := videoFilters(req)
		fallback := "b"
		if filters != "" {
			fallback += filters + "/b"
		}
		return video + "+mergeall[vcodec=none]/" + fallback
	}

	// Preserve one-sided explicit format choices. The container preference below
	// is only for automatic selection; otherwise a user-selected format could be
	// silently replaced by the MP4 fallback expression.
	if req.VideoFormatID != "" || req.AudioFormatID != "" {
		a := bestAudioSelector(req)
		if req.VideoFormatID != "" {
			return video + "+" + a + "/" + video + "/b"
		}
		return video + "+" + a + "/b"
	}

	// mp4 container: prefer H.264 + AAC (reliable merge into .mp4).
	// Otherwise allow any best video + best audio, with a progressive fallback.
	a := bestAudioSelector(req)
	filters := videoFilters(req)
	if req.Container == "mp4" && !req.PreferVP9 {
		mp4Video := "bv*[ext=mp4]"
		if req.Is360 {
			mp4Video += "[protocol=m3u8_native]"
		}
		if filters != "" {
			return mp4Video + filters + "+ba[ext=m4a]/bv*" + filters + "+ba/b" + filters + "/b"
		}
		return mp4Video + "+ba[ext=m4a]/bv*+ba/b"
	}

	if filters != "" {
		bFallback := "b" + filters + "/b"
		if a == "ba/b" {
			return video + "+ba/" + video + "/" + bFallback
		}
		return video + "+" + a + "/" + video + "/" + bFallback
	}

	if a == "ba/b" {
		return video + "+ba/b"
	}
	return video + "+" + a + "/b"
}

func videoAudioVideoSelector(req models.DownloadRequest) string {
	if req.VideoFormatID != "" {
		return req.VideoFormatID
	}
	return preferredVideoSelector(req, "bv*", videoFilters(req))
}

func preferredVideoSelector(req models.DownloadRequest, selector, filters string) string {
	if req.Is360 {
		return selector + "[protocol=m3u8_native]" + filters
	}
	return selector + filters
}

func videoFilters(req models.DownloadRequest) string {
	var filters strings.Builder
	if req.MaxHeight > 0 {
		filters.WriteString(fmt.Sprintf("[height<=?%d]", req.MaxHeight))
	}
	if req.MaxFPS > 0 {
		filters.WriteString(fmt.Sprintf("[fps<=?%d]", req.MaxFPS))
	}
	return filters.String()
}

func requiresMultiAudio(req models.DownloadRequest) bool {
	mode, selected := audioSelection(req)
	return mode == models.AudioSelectionAll || (mode == models.AudioSelectionCustom && len(selected) > 1)
}

var languageCodeRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z]{2,4})?$`)

func audioSelector(track string) string {
	if languageCodeRe.MatchString(track) {
		return "ba[language=" + track + "]"
	}
	return track
}

func audioSelection(req models.DownloadRequest) (models.AudioSelectionMode, []string) {
	selection := req.AudioSelectionMode
	if selection == "" {
		selection = req.AudioSelection
	}
	if selection != "" {
		switch selection {
		case models.AudioSelectionAll:
			return models.AudioSelectionAll, nil
		case models.AudioSelectionCustom:
			return models.AudioSelectionCustom, req.SelectedAudioTrackIDs
		case models.AudioSelectionNone, models.AudioSelectionDefault:
			return models.AudioSelectionNone, nil
		}
	}
	if req.MultiAudio {
		return models.AudioSelectionAll, nil
	}
	if len(req.SelectedAudioTrackIDs) > 0 {
		return models.AudioSelectionCustom, req.SelectedAudioTrackIDs
	}
	return models.AudioSelectionNone, nil
}

func subtitleSelection(req models.DownloadRequest, settings config.AppSettings) (models.SubtitleSelectionMode, []string) {
	selection := req.SubtitleSelectionMode
	if selection == "" {
		selection = req.SubtitleSelection
	}
	if selection != "" {
		switch selection {
		case models.SubtitleSelectionAll:
			return models.SubtitleSelectionAll, nil
		case models.SubtitleSelectionCustom:
			if len(req.SubtitleLangs) > 0 {
				return models.SubtitleSelectionCustom, req.SubtitleLangs
			}
			return models.SubtitleSelectionNone, nil
		case models.SubtitleSelectionNone:
			return models.SubtitleSelectionNone, nil
		case models.SubtitleSelectionDefault:
			// Continue with the legacy fields below.
		}
	}

	if !req.WriteSubtitles && !settings.WriteSubtitles {
		return models.SubtitleSelectionNone, nil
	}
	langs := req.SubtitleLangs
	if len(langs) == 0 {
		langs = settings.SubtitleLangs
	}
	if len(langs) == 0 || contains(langs, "all") {
		return models.SubtitleSelectionAll, nil
	}
	return models.SubtitleSelectionCustom, langs
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func isDirectStreamURL(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	return strings.Contains(lower, ".m3u8") || strings.Contains(lower, ".mpd") || strings.Contains(lower, "aryzap.com")
}

func (s *YtDlpService) run(ctx context.Context, args []string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, s.ytdlpPath, args...) // #nosec G204 -- exec.CommandContext passes argv directly without a shell.
	prepareBackgroundCmd(cmd)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	diagnostics := &boundedDiagnosticBuffer{limit: maxYtDlpDiagnosticBytes}
	cmd.Stderr = diagnostics
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		msg := strings.TrimSpace(diagnostics.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return stdout.Bytes(), nil
}

// ---------- parsers ----------

func parseMedia(raw map[string]any, originalURL string) *models.MediaInfo {
	m := &models.MediaInfo{
		OriginalURL:       originalURL,
		Subtitles:         map[string][]models.SubtitleTrack{},
		AutomaticCaptions: map[string][]models.SubtitleTrack{},
	}
	m.ID, _ = raw["id"].(string)
	m.Title, _ = raw["title"].(string)
	m.Description, _ = raw["description"].(string)
	m.Uploader, _ = raw["uploader"].(string)
	m.Thumbnail, _ = raw["thumbnail"].(string)
	m.WebpageURL, _ = raw["webpage_url"].(string)
	m.Extractor, _ = raw["extractor"].(string)
	m.Is360 = is360Video(raw)
	if d, ok := raw["duration"].(float64); ok {
		m.Duration = d
	}
	m.IsLive, _ = raw["is_live"].(bool)

	if formats, ok := raw["formats"].([]any); ok {
		audioTrackIndexes := make(map[string]int)
		for _, f := range formats {
			fm, _ := f.(map[string]any)
			fi := parseFormat(fm)
			m.Formats = append(m.Formats, fi)
			if fi.IsAudio && !fi.IsVideo {
				track := models.AudioTrack{
					ID:         fi.ID,
					Language:   fi.Language,
					Codec:      fi.ACodec,
					Bitrate:    fi.ABR,
					Note:       fi.Note,
					IsOriginal: fi.IsOriginal,
				}
				if !track.IsOriginal && track.IsOriginalTrack() {
					track.IsOriginal = true
				}
				if track.Bitrate <= 0 {
					track.Bitrate = fi.TBR
				}
				// A rich extraction returns several technical formats for the
				// same logical language. Keep the best one in the picker rather
				// than showing every bitrate/codec variant as a separate track.
				key := track.Language
				if note := strings.TrimSpace(track.Note); note != "" {
					key += "\x00" + note
				}
				if previous, exists := audioTrackIndexes[key]; exists {
					if preferAudioTrack(track, m.AudioTracks[previous]) {
						m.AudioTracks[previous] = track
					}
				} else {
					audioTrackIndexes[key] = len(m.AudioTracks)
					m.AudioTracks = append(m.AudioTracks, track)
				}
			}
		}
	}
	parseSubs(raw["subtitles"], m.Subtitles)
	parseSubs(raw["automatic_captions"], m.AutomaticCaptions)
	return m
}

func is360Video(raw map[string]any) bool {
	for _, key := range []string{"projection", "stereo_mode"} {
		value, _ := raw[key].(string)
		value = strings.ToLower(strings.TrimSpace(value))
		if strings.Contains(value, "equirectangular") || strings.Contains(value, "spherical") || strings.Contains(value, "mesh") {
			return true
		}
	}

	formats, _ := raw["formats"].([]any)
	for _, item := range formats {
		format, _ := item.(map[string]any)
		note, _ := format["format_note"].(string)
		if strings.Contains(strings.ToLower(note), "mesh") {
			return true
		}
		streamURL, _ := format["url"].(string)
		streamURL = strings.ToLower(streamURL)
		if strings.Contains(streamURL, "vproj=mesh") || strings.Contains(streamURL, "vproj%3dmesh") {
			return true
		}
	}
	return false
}

func preferAudioTrack(candidate, current models.AudioTrack) bool {
	if candidate.IsOriginalTrack() != current.IsOriginalTrack() {
		return candidate.IsOriginalTrack()
	}
	candidateOpus := strings.Contains(strings.ToLower(candidate.Codec), "opus")
	currentOpus := strings.Contains(strings.ToLower(current.Codec), "opus")
	if candidateOpus != currentOpus {
		return candidateOpus
	}
	if candidate.Bitrate != current.Bitrate {
		return candidate.Bitrate > current.Bitrate
	}
	return candidate.ID > current.ID
}

func parseFormat(fm map[string]any) models.FormatInfo {
	fi := models.FormatInfo{}
	fi.ID, _ = fm["format_id"].(string)
	fi.Ext, _ = fm["ext"].(string)
	fi.Resolution, _ = fm["resolution"].(string)
	fi.VCodec, _ = fm["vcodec"].(string)
	fi.ACodec, _ = fm["acodec"].(string)
	fi.Note, _ = fm["format_note"].(string)
	fi.Protocol, _ = fm["protocol"].(string)
	fi.Language, _ = fm["language"].(string)
	if orig, ok := fm["is_original"].(bool); ok && orig {
		fi.IsOriginal = true
	}
	if v, ok := fm["fps"].(float64); ok {
		fi.FPS = v
	}
	if v, ok := fm["filesize"].(float64); ok {
		fi.Filesize = int64(v)
	}
	if v, ok := fm["filesize_approx"].(float64); ok {
		fi.FilesizeApprox = int64(v)
	}
	if v, ok := fm["tbr"].(float64); ok {
		fi.TBR = v
	}
	if v, ok := fm["abr"].(float64); ok {
		fi.ABR = v
	}
	if v, ok := fm["vbr"].(float64); ok {
		fi.VBR = v
	}
	if v, ok := fm["height"].(float64); ok {
		fi.Height = int(v)
	}
	if v, ok := fm["width"].(float64); ok {
		fi.Width = int(v)
	}
	fi.IsVideo = fi.VCodec != "" && fi.VCodec != "none"
	fi.IsAudio = fi.ACodec != "" && fi.ACodec != "none"
	fi.HasAudio = fi.IsAudio
	return fi
}

func parseSubs(raw any, dest map[string][]models.SubtitleTrack) {
	m, ok := raw.(map[string]any)
	if !ok {
		return
	}
	for lang, list := range m {
		lang = strings.TrimSpace(lang)
		cleanLower := strings.ToLower(lang)
		if lang == "" || cleanLower == "und" || cleanLower == "unknown" || cleanLower == "none" || strings.EqualFold(lang, "live_chat") {
			continue
		}
		arr, _ := list.([]any)
		for _, item := range arr {
			sm, ok := item.(map[string]any)
			if !ok {
				continue
			}
			st := models.SubtitleTrack{Lang: lang}
			st.Name, _ = sm["name"].(string)
			st.Ext, _ = sm["ext"].(string)
			st.URL, _ = sm["url"].(string)
			dest[lang] = append(dest[lang], st)
		}
	}
}

func parsePlaylist(raw map[string]any, originalURL string) *models.PlaylistInfo {
	p := &models.PlaylistInfo{OriginalURL: originalURL}
	p.ID, _ = raw["id"].(string)
	p.Title, _ = raw["title"].(string)
	p.Uploader, _ = raw["uploader"].(string)
	p.WebpageURL, _ = raw["webpage_url"].(string)
	if entries, ok := raw["entries"].([]any); ok {
		for i, e := range entries {
			em, _ := e.(map[string]any)
			if em == nil {
				continue
			}
			ent := models.PlaylistEntry{Index: i + 1}
			ent.ID, _ = em["id"].(string)
			ent.Title, _ = em["title"].(string)
			ent.URL, _ = em["url"].(string)
			if ent.URL == "" {
				ent.URL, _ = em["webpage_url"].(string)
			}
			if d, ok := em["duration"].(float64); ok {
				ent.Duration = d
			}
			ent.Thumbnail, _ = em["thumbnail"].(string)
			p.Entries = append(p.Entries, ent)
		}
	}
	return p
}

const progressMarker = "PROGRESS|"

// parseProgressLine parses the literal marker emitted by downloadArgs. The
// `download:` part of --progress-template is a selector understood by yt-dlp,
// not text that yt-dlp writes to stdout. The seven-field form is retained for
// compatibility with older helper output; new output includes status first.
func parseProgressLine(line string) (models.ProgressInfo, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, progressMarker) {
		return models.ProgressInfo{}, false
	}
	fields := strings.Split(strings.TrimPrefix(line, progressMarker), "|")
	var status string
	if len(fields) == 8 {
		status = strings.TrimSpace(fields[0])
		fields = fields[1:]
	}
	if len(fields) != 7 {
		return models.ProgressInfo{}, false
	}
	p := models.ProgressInfo{Status: status}
	p.DownloadedBytes = parseProgressInt64(fields[0])
	if total := parseProgressInt64(fields[1]); total > 0 {
		p.TotalBytes = total
	} else if estimate := parseProgressInt64(fields[2]); estimate > 0 {
		p.TotalBytes = estimate
		p.TotalIsEstimate = true
	}
	p.SpeedBps = parseProgressFloat(fields[3])
	if seconds := parseProgressFloat(fields[4]); seconds >= 0 {
		p.ETA = time.Duration(seconds * float64(time.Second))
	}
	p.FragmentIndex = parseProgressInt(fields[5])
	p.FragmentCount = parseProgressInt(fields[6])
	if p.TotalBytes > 0 && p.DownloadedBytes >= 0 {
		p.Percent = float64(p.DownloadedBytes) / float64(p.TotalBytes) * 100
	}
	return p, true
}

func parseProgressInt64(value string) int64 {
	value = strings.TrimSpace(value)
	if isUnavailableProgressValue(value) {
		return 0
	}
	if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed >= 0 {
		return parsed
	}
	if parsedFloat, err := strconv.ParseFloat(value, 64); err == nil && parsedFloat >= 0 {
		return int64(math.Round(parsedFloat))
	}
	return 0
}

func parseProgressInt(value string) int {
	parsed := parseProgressInt64(value)
	if parsed > int64(^uint(0)>>1) {
		return 0
	}
	return int(parsed)
}

func parseProgressFloat(value string) float64 {
	value = strings.TrimSpace(value)
	if isUnavailableProgressValue(value) {
		return 0
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

func isUnavailableProgressValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "na", "none", "null", "n/a":
		return true
	default:
		return false
	}
}

func newestFile(dir string) string {
	var newest string
	var newestMod time.Time
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if info.ModTime().After(newestMod) {
			newestMod = info.ModTime()
			newest = path
		}
		return nil
	})
	return newest
}
