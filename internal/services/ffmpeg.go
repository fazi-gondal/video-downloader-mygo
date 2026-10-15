package services

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
	"github.com/fazi-gondal/video-downloader-mygo/internal/tools"
)

// FFmpegService locates ffmpeg/ffprobe and runs conversions.
type FFmpegService struct {
	ffmpeg     string
	ffprobe    string
	remuxMu    sync.Mutex
	remuxCache map[string]remuxCacheEntry
}

type remuxCacheEntry struct {
	size    int64
	modTime time.Time
	result  bool
}

func NewFFmpegService() *FFmpegService {
	return &FFmpegService{
		ffmpeg:     tools.FFmpeg(),
		ffprobe:    tools.FFprobe(),
		remuxCache: make(map[string]remuxCacheEntry),
	}
}

func (s *FFmpegService) Available() bool {
	return s.ffmpeg != ""
}

func (s *FFmpegService) Path() string {
	return s.ffmpeg
}

// CanRemux conservatively reports whether a container-only conversion is a
// safe choice. Same-container copies are always safe; uncertain container or
// codec combinations remain re-encodes so the converter never promises a
// lossless result that FFmpeg cannot produce.
func (s *FFmpegService) CanRemux(source, target string) bool {
	srcExt := normalizeFormat(filepath.Ext(source))
	targetExt := normalizeFormat(target)
	if !isSupportedVideoFormat(targetExt) || srcExt == "" {
		return false
	}
	if srcExt == targetExt {
		return true
	}

	info, err := os.Stat(source)
	if err != nil || info.IsDir() {
		return fallbackRemuxDecision(srcExt, targetExt)
	}
	cacheKey := source + "\x00" + targetExt
	if result, ok := s.cachedRemuxDecision(cacheKey, info); ok {
		return result
	}

	result := fallbackRemuxDecision(srcExt, targetExt)
	if s.ffprobe != "" {
		if streams, ok := s.probeCodecs(source); ok {
			result = codecsFitContainer(streams, targetExt)
		}
	}
	s.cacheRemuxDecision(cacheKey, info, result)
	return result
}

// Convert runs a conversion job. onProgress receives 0–100.
func (s *FFmpegService) Convert(ctx context.Context, job *models.ConversionJob, onProgress func(float64)) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.ffmpeg == "" {
		return "", fmt.Errorf("ffmpeg not found on PATH")
	}
	req := job.Request
	src := req.Source
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("source not found: %s", src)
	}

	ext := strings.TrimPrefix(req.TargetFormat, ".")
	if ext == "" {
		ext = "mp4"
	}
	ext = normalizeFormat(ext)
	out := uniqueOutputPath(src, ext)

	req.TargetFormat = ext
	args, err := conversionArgs(req, out)
	if err != nil {
		return "", err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
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
	defer func() {
		close(watchDone)
		cancel()
		<-watchExited
	}()

	cmd := exec.CommandContext(runCtx, s.ffmpeg, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}

	if err := cmd.Start(); err != nil {
		return "", err
	}
	stderrBuffer := &boundedDiagnosticBuffer{limit: maxYtDlpDiagnosticBytes}
	stderrDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(stderrBuffer, stderr)
		stderrDone <- err
	}()

	// duration for percent (best-effort via ffprobe)
	duration := s.probeDuration(runCtx, src)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if job.Cancelled() {
			cancel()
		}
		line := scanner.Text()
		if strings.HasPrefix(line, "out_time_ms=") {
			ms, _ := strconv.ParseInt(strings.TrimPrefix(line, "out_time_ms="), 10, 64)
			if duration > 0 && onProgress != nil {
				pct := float64(ms) / 1000.0 / duration * 100
				if pct > 100 {
					pct = 100
				}
				onProgress(pct)
			}
		}
	}

	scanErr := scanner.Err()
	if scanErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	stderrErr := <-stderrDone
	if scanErr != nil {
		_ = os.Remove(out)
		return "", fmt.Errorf("read ffmpeg progress: %w", scanErr)
	}
	if stderrErr != nil && waitErr == nil {
		_ = os.Remove(out)
		return "", fmt.Errorf("read ffmpeg diagnostics: %w", stderrErr)
	}
	if waitErr != nil {
		_ = os.Remove(out)
		if job.Cancelled() || runCtx.Err() != nil {
			return "", context.Canceled
		}
		msg := strings.TrimSpace(stderrBuffer.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		// last line often has the real error
		lines := strings.Split(msg, "\n")
		if len(lines) > 0 {
			msg = lines[len(lines)-1]
		}
		return "", fmt.Errorf("%s", msg)
	}
	if info, err := os.Stat(out); err != nil || info.IsDir() || info.Size() == 0 {
		_ = os.Remove(out)
		if err != nil {
			return "", fmt.Errorf("ffmpeg did not create output: %w", err)
		}
		return "", fmt.Errorf("ffmpeg created an empty output")
	}
	if onProgress != nil {
		onProgress(100)
	}
	if !req.KeepOriginal {
		_ = os.Remove(src)
	}
	return out, nil
}

func uniqueOutputPath(src, ext string) string {
	outDir := filepath.Dir(src)
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	candidate := filepath.Join(outDir, base+"."+ext)
	if candidate == src || fileMissing(candidate) {
		if candidate != src {
			return candidate
		}
		candidate = filepath.Join(outDir, base+"_converted."+ext)
		if fileMissing(candidate) {
			return candidate
		}
	}
	for i := 2; ; i++ {
		candidate = filepath.Join(outDir, fmt.Sprintf("%s_%d.%s", base, i, ext))
		if fileMissing(candidate) {
			return candidate
		}
	}
}

func fileMissing(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

func (s *FFmpegService) probeDuration(ctx context.Context, path string) float64 {
	if s.ffprobe == "" {
		return 0
	}
	cmd := exec.CommandContext(ctx, s.ffprobe, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	d, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return d
}

const maxAudioBitrateKbps = 320

var losslessAudioFormats = map[string]struct{}{
	"flac": {},
	"wav":  {},
}

var supportedAudioFormats = map[string]struct{}{
	"aac":  {},
	"flac": {},
	"m4a":  {},
	"mp3":  {},
	"opus": {},
	"wav":  {},
}

var supportedVideoFormats = map[string]struct{}{
	"avi":  {},
	"mkv":  {},
	"mov":  {},
	"mp4":  {},
	"ts":   {},
	"webm": {},
}

// conversionArgs returns the complete FFmpeg argument list for a conversion.
// Keeping argument selection separate from process execution makes codec and
// bitrate behavior testable without requiring media files or an FFmpeg binary.
func conversionArgs(req models.ConversionRequest, output string) ([]string, error) {
	ext := normalizeFormat(req.TargetFormat)
	if ext == "" {
		ext = "mp4"
	}
	args := []string{"-y", "-i", req.Source, "-progress", "pipe:1", "-nostats"}

	switch req.Kind {
	case models.KindAudio:
		if !isSupportedAudioFormat(ext) {
			return nil, fmt.Errorf("unsupported audio format %q", ext)
		}
		if req.AudioBitrateKbps < 0 {
			return nil, fmt.Errorf("audio bitrate cannot be negative")
		}
		args = append(args, "-vn")
		args = append(args, audioCodecArgs(ext)...)
		if !isLosslessAudioFormat(ext) && req.AudioBitrateKbps > 0 {
			if req.AudioBitrateKbps > maxAudioBitrateKbps {
				return nil, fmt.Errorf("audio bitrate cannot exceed %d kbps", maxAudioBitrateKbps)
			}
			args = append(args, "-b:a", strconv.Itoa(req.AudioBitrateKbps)+"k")
		}
	case models.KindVideo, "":
		if !isSupportedVideoFormat(ext) {
			return nil, fmt.Errorf("unsupported video format %q", ext)
		}
		if req.Mode == models.ConversionRemux {
			args = append(args, "-c", "copy")
		} else {
			args = append(args, videoCodecArgs(ext)...)
		}
	default:
		return nil, fmt.Errorf("unsupported media kind %q", req.Kind)
	}

	return append(args, output), nil
}

func audioCodecArgs(ext string) []string {
	switch normalizeFormat(ext) {
	case "mp3":
		return []string{"-c:a", "libmp3lame"}
	case "m4a", "aac":
		return []string{"-c:a", "aac"}
	case "flac":
		return []string{"-c:a", "flac"}
	case "opus":
		return []string{"-c:a", "libopus"}
	case "wav":
		return []string{"-c:a", "pcm_s16le"}
	default:
		return nil
	}
}

func videoCodecArgs(ext string) []string {
	switch normalizeFormat(ext) {
	case "webm":
		// WebM does not accept H.264/AAC. VP9/Opus is broadly supported and
		// the zero video bitrate selects constant-quality VP9 encoding.
		return []string{
			"-c:v", "libvpx-vp9",
			"-crf", "32",
			"-b:v", "0",
			"-row-mt", "1",
			"-threads", "0",
			"-speed", "4",
			"-c:a", "libopus",
		}
	case "avi":
		return []string{
			"-c:v", "mpeg4",
			"-q:v", "4",
			"-c:a", "libmp3lame",
			"-q:a", "3",
		}
	case "mp4", "mkv", "mov", "ts":
		return []string{
			"-c:v", "libx264",
			"-preset", "medium",
			"-crf", "23",
			"-c:a", "aac",
			"-b:a", "192k",
		}
	default:
		return nil
	}
}

func normalizeFormat(format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	return strings.TrimPrefix(format, ".")
}

func isSupportedAudioFormat(format string) bool {
	_, ok := supportedAudioFormats[normalizeFormat(format)]
	return ok
}

func isSupportedVideoFormat(format string) bool {
	_, ok := supportedVideoFormats[normalizeFormat(format)]
	return ok
}

func isLosslessAudioFormat(format string) bool {
	_, ok := losslessAudioFormats[normalizeFormat(format)]
	return ok
}

var fallbackRemuxTargets = map[string]map[string]struct{}{
	"avi":  {"avi": {}},
	"mkv":  {"mkv": {}},
	"mov":  {"mov": {}},
	"mp4":  {"mkv": {}, "mp4": {}},
	"ts":   {"ts": {}},
	"webm": {"mkv": {}, "webm": {}},
}

func fallbackRemuxDecision(source, target string) bool {
	targets := fallbackRemuxTargets[normalizeFormat(source)]
	_, ok := targets[normalizeFormat(target)]
	return ok
}

type probedStream struct {
	CodecType string `json:"codec_type"`
	CodecName string `json:"codec_name"`
}

type probeResult struct {
	Streams []probedStream `json:"streams"`
}

func (s *FFmpegService) probeCodecs(path string) ([]probedStream, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(
		ctx,
		s.ffprobe,
		"-v", "error",
		"-show_entries", "stream=codec_type,codec_name",
		"-of", "json",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var result probeResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, false
	}
	return result.Streams, true
}

var containerVideoCodecs = map[string]map[string]struct{}{
	"avi": {"mjpeg": {}, "mpeg4": {}, "msmpeg4v3": {}},
	"mkv": {
		"av1": {}, "dnxhd": {}, "dnxhr": {}, "h264": {}, "hevc": {},
		"mpeg2video": {}, "mpeg4": {}, "mjpeg": {}, "prores": {},
		"prores_ks": {}, "theora": {}, "vp8": {}, "vp9": {},
	},
	"mov": {
		"av1": {}, "dnxhd": {}, "dnxhr": {}, "h264": {}, "hevc": {},
		"mpeg4": {}, "mjpeg": {}, "prores": {}, "prores_ks": {},
	},
	"mp4":  {"av1": {}, "h264": {}, "hevc": {}, "mpeg4": {}},
	"ts":   {"av1": {}, "h264": {}, "hevc": {}, "mpeg2video": {}, "mpeg4": {}},
	"webm": {"av1": {}, "vp8": {}, "vp9": {}},
}

var containerAudioCodecs = map[string]map[string]struct{}{
	"avi": {"ac3": {}, "mp3": {}, "pcm_s16le": {}},
	"mkv": {
		"aac": {}, "ac3": {}, "alac": {}, "dts": {}, "eac3": {},
		"flac": {}, "mp3": {}, "opus": {}, "pcm_s16le": {}, "vorbis": {},
	},
	"mov": {
		"aac": {}, "ac3": {}, "alac": {}, "mp3": {}, "opus": {},
		"pcm_s16le": {}, "pcm_s24le": {}, "pcm_s32le": {},
	},
	"mp4":  {"aac": {}, "ac3": {}, "alac": {}, "mp3": {}, "opus": {}},
	"ts":   {"aac": {}, "ac3": {}, "eac3": {}, "mp2": {}, "mp3": {}},
	"webm": {"opus": {}, "vorbis": {}},
}

func codecsFitContainer(streams []probedStream, target string) bool {
	videoCodecs, videoOK := containerVideoCodecs[normalizeFormat(target)]
	audioCodecs, audioOK := containerAudioCodecs[normalizeFormat(target)]
	if !videoOK || !audioOK {
		return false
	}
	for _, stream := range streams {
		codec := strings.ToLower(stream.CodecName)
		switch stream.CodecType {
		case "video":
			if _, ok := videoCodecs[codec]; !ok {
				return false
			}
		case "audio":
			if _, ok := audioCodecs[codec]; !ok {
				return false
			}
		}
	}
	return true
}

func (s *FFmpegService) cachedRemuxDecision(key string, info os.FileInfo) (bool, bool) {
	s.remuxMu.Lock()
	defer s.remuxMu.Unlock()
	entry, ok := s.remuxCache[key]
	if !ok || entry.size != info.Size() || !entry.modTime.Equal(info.ModTime()) {
		return false, false
	}
	return entry.result, true
}

func (s *FFmpegService) cacheRemuxDecision(key string, info os.FileInfo, result bool) {
	s.remuxMu.Lock()
	defer s.remuxMu.Unlock()
	if s.remuxCache == nil {
		s.remuxCache = make(map[string]remuxCacheEntry)
	}
	s.remuxCache[key] = remuxCacheEntry{
		size:    info.Size(),
		modTime: info.ModTime(),
		result:  result,
	}
}

var timeRe = regexp.MustCompile(`time=(\d+):(\d+):(\d+\.\d+)`)

func parseFFmpegTime(line string) time.Duration {
	m := timeRe.FindStringSubmatch(line)
	if m == nil {
		return 0
	}
	h, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	sec, _ := strconv.ParseFloat(m[3], 64)
	return time.Duration(h)*time.Hour + time.Duration(min)*time.Minute + time.Duration(sec*float64(time.Second))
}

func lookPath(file string) (string, error) {
	path := os.Getenv("PATH")
	for _, dir := range strings.Split(path, string(os.PathListSeparator)) {
		p := filepath.Join(dir, file)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
		// Windows
		if st, err := os.Stat(p + ".exe"); err == nil && !st.IsDir() {
			return p + ".exe", nil
		}
	}
	return "", fmt.Errorf("%s not found", file)
}
