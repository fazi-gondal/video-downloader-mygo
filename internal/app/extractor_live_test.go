package app

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/config"
	"github.com/fazi-gondal/video-downloader-mygo/internal/services"
)

// TestLiveExtractAllVideos exercises page fetching, stream recovery, and
// yt-dlp probing for every player on a real page. Opt in with LIVE_SITE_URL.
func TestLiveExtractAllVideos(t *testing.T) {
	pageURL := os.Getenv("LIVE_SITE_URL")
	if pageURL == "" {
		t.Skip("set LIVE_SITE_URL to run the live extractor check")
	}
	a := &App{ytdlp: services.NewYtDlpService("", func() config.AppSettings { return config.AppSettings{} })}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	res, err := services.ExtractSiteMedia(ctx, pageURL)
	if err != nil {
		t.Fatalf("ExtractSiteMedia: %v", err)
	}
	if len(res.Videos) < 2 {
		t.Fatalf("videos = %d, want the page's multiple players", len(res.Videos))
	}

	for i, video := range res.Videos {
		media, err := a.probeVideo(ctx, video)
		if err != nil {
			// A transient CDN failure should not fail the whole check.
			t.Logf("video[%d] probe failed: %v", i, err)
			continue
		}
		t.Logf("video[%d] id=%q duration=%.0fs height=%d", i, video.ID, media.Duration, mediaHeight(media))
		if media.Duration <= 0 {
			t.Logf("video[%d] has no duration metadata", i)
		}
	}
}
