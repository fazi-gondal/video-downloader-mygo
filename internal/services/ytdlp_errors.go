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
		if isYtDlpSubtitleError(lower) &&
			(strings.Contains(lower, "http error 429") || strings.Contains(lower, "http 429")) {
			return true
		}
	}
	return false
}

func ytDlpSubtitleErrorLines(stderr string) []string {
	var subtitleLines []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && isYtDlpSubtitleError(strings.ToLower(line)) {
			subtitleLines = append(subtitleLines, line)
		}
	}
	return subtitleLines
}

func ytDlpNonSubtitleErrorLines(stderr string) []string {
	var nonSubtitleLines []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if !ytDlpFailureDiagnostic(line) {
			continue
		}
		if !isYtDlpSubtitleError(strings.ToLower(line)) {
			nonSubtitleLines = append(nonSubtitleLines, line)
		}
	}
	return nonSubtitleLines
}

func ytDlpOnlySubtitleErrors(stderr string) bool {
	var subtitleLines []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if ytDlpFailureDiagnostic(line) && isYtDlpSubtitleError(strings.ToLower(line)) {
			subtitleLines = append(subtitleLines, line)
		}
	}
	return len(subtitleLines) > 0 && len(ytDlpNonSubtitleErrorLines(stderr)) == 0
}

func ytDlpFailureDiagnostic(line string) bool {
	upper := strings.ToUpper(line)
	if strings.HasPrefix(upper, "ERROR:") {
		return true
	}
	if !strings.HasPrefix(upper, "WARNING:") {
		return false
	}
	lower := strings.ToLower(line)
	return strings.Contains(lower, "unable to download") ||
		strings.Contains(lower, "download failed") ||
		strings.Contains(lower, "post-process") ||
		strings.Contains(lower, "merging formats")
}

func ytDlpSubtitleWarning(stderr string) string {
	subtitleLines := ytDlpSubtitleErrorLines(stderr)
	if len(subtitleLines) == 0 {
		return ""
	}

	message := "Video/audio download completed, but one or more subtitles could not be downloaded. Retry later or select fewer subtitle languages."
	if ytDlpSubtitleRateLimit(subtitleLines) {
		message = subtitleRateLimitExplanation
	}
	return message + "\n\n" + strings.Join(subtitleLines, "\n")
}

func isYtDlpSubtitleError(lowerLine string) bool {
	return strings.Contains(lowerLine, "unable to download") && strings.Contains(lowerLine, "subtitle")
}
