package services

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
	"github.com/fazi-gondal/video-downloader-mygo/internal/tools"
)

// FFmpegService locates ffmpeg/ffprobe and runs conversions.
type FFmpegService struct {
	ffmpeg  string
	ffprobe string
}

func NewFFmpegService() *FFmpegService {
	return &FFmpegService{
		ffmpeg:  tools.FFmpeg(),
		ffprobe: tools.FFprobe(),
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
	srcExt := strings.TrimPrefix(strings.ToLower(filepath.Ext(source)), ".")
	targetExt := strings.TrimPrefix(strings.ToLower(target), ".")
	return srcExt != "" && targetExt != "" && srcExt == targetExt
}

// Convert runs a conversion job. onProgress receives 0–100.
func (s *FFmpegService) Convert(ctx context.Context, job *models.ConversionJob, onProgress func(float64)) (string, error) {
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
	out := uniqueOutputPath(src, ext)

	args := []string{"-y", "-i", src, "-progress", "pipe:1", "-nostats"}

	switch req.Kind {
	case models.KindAudio:
		args = append(args, audioCodecArgs(ext)...)
		if req.AudioBitrateKbps > 0 {
			args = append(args, "-b:a", strconv.Itoa(req.AudioBitrateKbps)+"k")
		}
		args = append(args, "-vn")
	default:
		if req.Mode == models.ConversionRemux {
			args = append(args, "-c", "copy")
		} else {
			// sensible defaults
			args = append(args, "-c:v", "libx264", "-preset", "medium", "-crf", "23")
			args = append(args, "-c:a", "aac", "-b:a", "192k")
		}
	}
	args = append(args, out)

	cmd := exec.CommandContext(ctx, s.ffmpeg, args...)
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
	var stderrBuffer bytes.Buffer
	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&stderrBuffer, stderr)
		close(stderrDone)
	}()

	// duration for percent (best-effort via ffprobe)
	duration := s.probeDuration(src)

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if job.Cancelled() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			<-stderrDone
			_ = os.Remove(out)
			return "", context.Canceled
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

	if scanErr := scanner.Err(); scanErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	<-stderrDone
	if waitErr != nil {
		_ = os.Remove(out)
		if job.Cancelled() {
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

func (s *FFmpegService) probeDuration(path string) float64 {
	if s.ffprobe == "" {
		return 0
	}
	cmd := exec.Command(s.ffprobe, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	d, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return d
}

func audioCodecArgs(ext string) []string {
	switch strings.ToLower(ext) {
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
		return []string{"-c:a", "aac"}
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
