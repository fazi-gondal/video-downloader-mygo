package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func requireAryPlusAPIKey(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("ARYPLUS_API_KEY")) == "" {
		t.Skip("ARYPLUS_API_KEY is not configured")
	}
}

func TestAryPlusCallAPIUsesDefaultAPIKey(t *testing.T) {
	var gotAPIKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]bool{"ok": true}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	t.Setenv("ARYPLUS_API_BASE", server.URL)
	t.Setenv("ARYPLUS_API_KEY", "")

	result, err := aryPlusCallAPI(context.Background(), "/test", url.Values{})
	if err != nil {
		t.Fatalf("aryPlusCallAPI() error = %v", err)
	}
	if result["ok"] != true {
		t.Errorf("aryPlusCallAPI() result = %v, want ok=true", result)
	}
	if gotAPIKey != aryPlusDefaultAPIKey {
		t.Errorf("x-api-key header was not set to the built-in default")
	}
}

func TestAryPlusURLMatching(t *testing.T) {
	tests := []struct {
		url       string
		isAryPlus bool
		isEpisode bool
		isSeries  bool
	}{
		{
			url:       "https://aryplus.tv/video/v2/3/6a6e0b1e9bc4b079859bc14b/6a57868b5bf57c474cc00a50",
			isAryPlus: true,
			isEpisode: true,
			isSeries:  false,
		},
		{
			url:       "https://www.aryplus.tv/video/v2/1/abcdef12345/6a57868b5bf57c474cc00a50/",
			isAryPlus: true,
			isEpisode: true,
			isSeries:  false,
		},
		{
			url:       "https://aryplus.tv/series/6a57868b5bf57c474cc00a50",
			isAryPlus: true,
			isEpisode: false,
			isSeries:  true,
		},
		{
			url:       "https://social.aryplus.tv/sharer/series/6a57868b5bf57c474cc00a50/",
			isAryPlus: true,
			isEpisode: false,
			isSeries:  true,
		},
		{
			url:       "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			isAryPlus: false,
			isEpisode: false,
			isSeries:  false,
		},
		{
			url:       "https://vod.aryzap.com/stream/adp.m3u8",
			isAryPlus: false,
			isEpisode: false,
			isSeries:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := IsAryPlusURL(tt.url); got != tt.isAryPlus {
				t.Errorf("IsAryPlusURL(%q) = %v, want %v", tt.url, got, tt.isAryPlus)
			}
			if got := IsAryPlusEpisodeURL(tt.url); got != tt.isEpisode {
				t.Errorf("IsAryPlusEpisodeURL(%q) = %v, want %v", tt.url, got, tt.isEpisode)
			}
			if got := IsAryPlusSeriesURL(tt.url); got != tt.isSeries {
				t.Errorf("IsAryPlusSeriesURL(%q) = %v, want %v", tt.url, got, tt.isSeries)
			}
		})
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Dar-E-Nijaat - Episode 1", "Dar-E-Nijaat - Episode 1"},
		{"Episode: Special / Part * 1?", "Episode_ Special _ Part _ 1_"},
		{"...test...", "test"},
		{"", "video"},
	}

	for _, tt := range tests {
		if got := sanitizeFilename(tt.input); got != tt.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestResolveAryPlusEpisodeLive(t *testing.T) {
	requireAryPlusAPIKey(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	url := "https://aryplus.tv/video/v2/3/6a6e0b1e9bc4b079859bc14b/6a57868b5bf57c474cc00a50"
	ep, err := ResolveAryPlusEpisode(ctx, url)
	if err != nil {
		t.Fatalf("ResolveAryPlusEpisode failed: %v", err)
	}

	if ep.VideoID != "6a6e0b1e9bc4b079859bc14b" {
		t.Errorf("VideoID = %q, want 6a6e0b1e9bc4b079859bc14b", ep.VideoID)
	}
	if !strings.Contains(ep.Title, "Dar-E-Nijaat") {
		t.Errorf("Title = %q, want it to contain 'Dar-E-Nijaat'", ep.Title)
	}
	if !strings.HasPrefix(ep.VideoSource, "https://") || !strings.Contains(ep.VideoSource, ".m3u8") {
		t.Errorf("VideoSource = %q, want valid .m3u8 URL", ep.VideoSource)
	}
	if ep.Thumbnail == "" || !strings.HasPrefix(ep.Thumbnail, "https://images.aryplus.tv/") {
		t.Errorf("Thumbnail = %q, want valid image URL", ep.Thumbnail)
	}
	if ep.Duration <= 0 {
		t.Errorf("Duration = %v, want > 0", ep.Duration)
	}
}

func TestFetchAryPlusSeriesPlaylistLive(t *testing.T) {
	requireAryPlusAPIKey(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	url := "https://aryplus.tv/series/6a57868b5bf57c474cc00a50"
	playlist, err := FetchAryPlusSeriesPlaylist(ctx, url)
	if err != nil {
		t.Fatalf("FetchAryPlusSeriesPlaylist failed: %v", err)
	}

	if playlist.ID != "6a57868b5bf57c474cc00a50" {
		t.Errorf("playlist ID = %q, want 6a57868b5bf57c474cc00a50", playlist.ID)
	}
	if playlist.Title != "Dar-E-Nijaat" {
		t.Errorf("playlist Title = %q, want 'Dar-E-Nijaat'", playlist.Title)
	}
	if len(playlist.Entries) == 0 {
		t.Fatalf("expected playlist entries, got 0")
	}

	first := playlist.Entries[0]
	if first.URL == "" || !strings.Contains(first.URL, "aryplus.tv/video/v2/3/") {
		t.Errorf("first entry URL = %q, want valid episode URL", first.URL)
	}
	if first.Thumbnail == "" {
		t.Errorf("first entry Thumbnail is empty")
	}
	if first.Duration <= 0 {
		t.Errorf("first entry Duration = %v, want > 0", first.Duration)
	}
}
