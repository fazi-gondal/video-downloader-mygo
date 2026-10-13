package services

import (
	"errors"
	"strings"
	"testing"
)

func TestYtDlpErrorMessage(t *testing.T) {
	tests := []struct {
		name       string
		stderr     string
		fallback   error
		want       string
		wantSubstr []string
		notContain []string
	}{
		{
			name: "fatal error wins over screenshot-like warnings",
			stderr: "WARNING: [youtube] token is missing\n" +
				"WARNING: [youtube] falling back to another client\n" +
				"ERROR: [youtube] en: Unable to download subtitles for 'en': HTTP Error 429: Too Many Requests",
			wantSubstr: []string{
				subtitleRateLimitExplanation,
				"ERROR: [youtube] en: Unable to download subtitles for 'en': HTTP Error 429: Too Many Requests",
			},
			notContain: []string{"token is missing", "falling back"},
		},
		{
			name: "multiple fatal errors are preserved",
			stderr: "WARNING: unrelated warning\n" +
				"ERROR: [site] first failure\n" +
				"ERROR: [site] second failure",
			want: "ERROR: [site] first failure\nERROR: [site] second failure",
		},
		{
			name:       "unknown stderr is preserved",
			stderr:     "unexpected yt-dlp failure",
			want:       "unexpected yt-dlp failure",
			notContain: []string{subtitleRateLimitExplanation},
		},
		{
			name:       "general video rate limit is not a subtitle diagnosis",
			stderr:     "ERROR: [youtube] unable to download video data: HTTP Error 429: Too Many Requests",
			want:       "ERROR: [youtube] unable to download video data: HTTP Error 429: Too Many Requests",
			notContain: []string{subtitleRateLimitExplanation, "subtitles"},
		},
		{
			name:     "fallback is used when stderr is empty",
			fallback: errors.New("process exited with status 1"),
			want:     "process exited with status 1",
		},
		{
			name: "nil fallback uses generic message",
			want: "yt-dlp download failed",
		},
		{
			name:   "subtitle rate limit matching is case insensitive",
			stderr: "eRrOr: [site] FR: uNaBlE tO dOwNlOaD sUbTiTlEs: hTtP 429 Too Many Requests",
			wantSubstr: []string{
				subtitleRateLimitExplanation,
				"eRrOr: [site] FR: uNaBlE tO dOwNlOaD sUbTiTlEs: hTtP 429 Too Many Requests",
			},
		},
		{
			name:       "warning subtitle rate limit is not fatal",
			stderr:     "WARNING: unable to download subtitles: HTTP Error 429",
			want:       "WARNING: unable to download subtitles: HTTP Error 429",
			notContain: []string{subtitleRateLimitExplanation},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ytDlpErrorMessage(tt.stderr, tt.fallback)
			if tt.want != "" && got != tt.want {
				t.Fatalf("ytDlpErrorMessage() = %q, want %q", got, tt.want)
			}
			for _, want := range tt.wantSubstr {
				if !strings.Contains(got, want) {
					t.Errorf("ytDlpErrorMessage() = %q, want substring %q", got, want)
				}
			}
			for _, unwanted := range tt.notContain {
				if strings.Contains(got, unwanted) {
					t.Errorf("ytDlpErrorMessage() = %q, must not contain %q", got, unwanted)
				}
			}
		})
	}
}
