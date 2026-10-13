package services

import "strings"

const subtitleRateLimitExplanation = "Subtitle server rate-limited this request (HTTP 429). Wait before retrying, select fewer subtitle languages or reduce simultaneous downloads, or explicitly select None to download without subtitles."

// ytDlpErrorMessage turns yt-dlp's stderr into a useful user-facing failure
// message. yt-dlp commonly emits warnings before the fatal error, so fatal
// ERROR: lines are preferred whenever they are present.
func ytDlpErrorMessage(stderr string, fallback error) string {
	trimmedStderr := strings.TrimSpace(stderr)
	fatalLines := ytDlpFatalErrorLines(stderr)

	message := strings.Join(fatalLines, "\n")
	if message == "" {
		message = trimmedStderr
	}
	if message == "" && fallback != nil {
		message = strings.TrimSpace(fallback.Error())
	}
	if message == "" {
		message = "yt-dlp download failed"
	}

	if ytDlpSubtitleRateLimit(fatalLines) {
		return subtitleRateLimitExplanation + "\n\n" + message
	}
	return message
}

func ytDlpFatalErrorLines(stderr string) []string {
	var fatalLines []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), "ERROR:") {
			fatalLines = append(fatalLines, line)
		}
	}
	return fatalLines
}

func ytDlpSubtitleRateLimit(fatalLines []string) bool {
	for _, line := range fatalLines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "unable to download subtitles") &&
			(strings.Contains(lower, "http error 429") || strings.Contains(lower, "http 429")) {
			return true
		}
	}
	return false
}
