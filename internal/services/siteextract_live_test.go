package services

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveExtractSiteMedia hits a real page. It is opt-in so the default test
// suite stays offline: set LIVE_SITE_URL to a page URL to run it.
func TestLiveExtractSiteMedia(t *testing.T) {
	pageURL := os.Getenv("LIVE_SITE_URL")
	if pageURL == "" {
		t.Skip("set LIVE_SITE_URL to run the live extractor check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	result, err := ExtractSiteMedia(ctx, pageURL)
	if err != nil {
		t.Fatalf("ExtractSiteMedia: %v", err)
	}
	if len(result.Videos) == 0 {
		t.Fatal("no videos extracted")
	}
	t.Logf("page title length=%d thumbnail=%q videos=%d", len(result.Title), result.Thumbnail, len(result.Videos))
	for i, video := range result.Videos {
		t.Logf("video[%d] id=%q titleLen=%d format=%q urlLen=%d", i, video.ID, len(video.Title), video.Format, len(video.MediaURL))
	}
}
