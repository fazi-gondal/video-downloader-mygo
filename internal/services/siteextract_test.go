package services

import (
	"errors"
	"strings"
	"testing"
)

const siteExtractFixture = `<!doctype html><html><head>
<title>Fallback title</title>
<meta property="og:title" content="Fixture &amp; title">
<meta property="og:image" content="https://img.example.com/cover.jpg">
</head><body>
<div class="dplayer" data-video_id="111" data-video_title="Part one"></div>
<div class="dplayer-loading"><div class="dplayer-loading-text">loading…</div></div>
<script>
  window.player = {"player_ads":[],"video":{"url":"https:\/\/hls.example.com\/videos5\/aaa\/aaa.m3u8?auth_key=1-2-3&v=3","type":"hls","thumbnails":null}};
</script>
<div class="dplayer" data-video_id="222" data-video_title="Part &amp; two"></div>
<script>
  window.player = {"player_ads":[],"video":{"url":"https:\/\/hls.example.com\/videos5\/bbb\/bbb.m3u8?auth_key=4-5-6&v=3","type":"hls","thumbnails":null}};
</script>
</body></html>`

func TestParseSitePageExtractsEveryPlayer(t *testing.T) {
	result, err := parseSitePage("https://51cg1.com/archives/1/", siteExtractFixture)
	if err != nil {
		t.Fatalf("parseSitePage: %v", err)
	}
	if got := len(result.Videos); got != 2 {
		t.Fatalf("Videos = %d, want 2", got)
	}
	wantURLs := []string{
		"https://hls.example.com/videos5/aaa/aaa.m3u8?auth_key=1-2-3&v=3",
		"https://hls.example.com/videos5/bbb/bbb.m3u8?auth_key=4-5-6&v=3",
	}
	wantTitles := []string{"Part one", "Part & two"}
	wantIDs := []string{"111", "222"}
	for i, video := range result.Videos {
		if video.MediaURL != wantURLs[i] {
			t.Fatalf("Videos[%d].MediaURL = %q, want %q", i, video.MediaURL, wantURLs[i])
		}
		if video.Title != wantTitles[i] {
			t.Fatalf("Videos[%d].Title = %q, want %q", i, video.Title, wantTitles[i])
		}
		if video.ID != wantIDs[i] {
			t.Fatalf("Videos[%d].ID = %q, want %q", i, video.ID, wantIDs[i])
		}
		if video.Format != "hls" {
			t.Fatalf("Videos[%d].Format = %q, want hls", i, video.Format)
		}
	}
	if result.Title != "Fixture & title" {
		t.Fatalf("Title = %q", result.Title)
	}
	if result.Thumbnail != "https://img.example.com/cover.jpg" {
		t.Fatalf("Thumbnail = %q", result.Thumbnail)
	}
}

func TestParseSitePageDeduplicatesStreams(t *testing.T) {
	body := `<script>
{"video":{"url":"https:\/\/cdn.example.com\/same.m3u8","type":"hls"}}
{"video":{"url":"https:\/\/cdn.example.com\/same.m3u8","type":"hls"}}
</script>`
	result, err := parseSitePage("https://example.com/page", body)
	if err != nil {
		t.Fatalf("parseSitePage: %v", err)
	}
	if len(result.Videos) != 1 {
		t.Fatalf("Videos = %d, want 1", len(result.Videos))
	}
}

func TestParseSitePageFallsBackToH265(t *testing.T) {
	body := `<script>{"video_h265":{"url":"https:\/\/cdn.example.com\/hevc.m3u8","type":"hls"}}</script>`
	result, err := parseSitePage("https://example.com/page", body)
	if err != nil {
		t.Fatalf("parseSitePage: %v", err)
	}
	if len(result.Videos) != 1 || !strings.Contains(result.Videos[0].MediaURL, "hevc.m3u8") {
		t.Fatalf("Videos = %#v, want the H.265 stream", result.Videos)
	}
}

func TestParseSitePageLeavesUnlabelledTitlesEmpty(t *testing.T) {
	body := `<script>
{"video":{"url":"https:\/\/cdn.example.com\/a.m3u8"}}
{"video":{"url":"https:\/\/cdn.example.com\/b.m3u8"}}
</script>`
	result, err := parseSitePage("https://example.com/page", body)
	if err != nil {
		t.Fatalf("parseSitePage: %v", err)
	}
	for i, video := range result.Videos {
		if video.Title != "" {
			t.Fatalf("Videos[%d].Title = %q, want empty (localized by the UI)", i, video.Title)
		}
	}
}

func TestParseSitePageReportsEncryptedConfig(t *testing.T) {
	_, err := parseSitePage("https://example.com/page", `<script>encryptedConfig:{encryptedData:'AAAA'}</script>`)
	if !errors.Is(err, ErrEncryptedConfig) {
		t.Fatalf("err = %v, want ErrEncryptedConfig", err)
	}
}

func TestParseSitePageReportsMissingMedia(t *testing.T) {
	_, err := parseSitePage("https://example.com/page", `<html><body>nothing here</body></html>`)
	if !errors.Is(err, ErrNoPlayableMedia) {
		t.Fatalf("err = %v, want ErrNoPlayableMedia", err)
	}
}

func TestExtractSiteMediaRejectsNonHTTP(t *testing.T) {
	if _, err := ExtractSiteMedia(t.Context(), "file:///etc/passwd"); err == nil {
		t.Fatal("expected error for non-HTTP URL")
	}
}

func TestParseSitePageKeepsPlayersThatOnlyShipH265(t *testing.T) {
	body := `<div class="dplayer" data-video_id="1" data-video_title="A"></div>
<script>{"video":{"url":"https:\/\/cdn.example.com\/a.m3u8"}}</script>
<div class="dplayer" data-video_id="2" data-video_title="B"></div>
<script>{"video_h265":{"url":"https:\/\/cdn.example.com\/b265.m3u8"}}</script>`
	result, err := parseSitePage("https://example.com/page", body)
	if err != nil {
		t.Fatalf("parseSitePage: %v", err)
	}
	if len(result.Videos) != 2 {
		t.Fatalf("Videos = %d, want 2", len(result.Videos))
	}
	if result.Videos[1].Title != "B" || !strings.Contains(result.Videos[1].MediaURL, "b265") {
		t.Fatalf("second video = %#v", result.Videos[1])
	}
}

func TestParseSitePagePrefersPrimaryForSamePlayer(t *testing.T) {
	body := `<div class="dplayer" data-video_id="1" data-video_title="A"></div>
<script>{"video":{"url":"https:\/\/cdn.example.com\/a.m3u8"},"video_h265":{"url":"https:\/\/cdn.example.com\/a265.m3u8"}}</script>`
	result, err := parseSitePage("https://example.com/page", body)
	if err != nil {
		t.Fatalf("parseSitePage: %v", err)
	}
	if len(result.Videos) != 1 || strings.Contains(result.Videos[0].MediaURL, "265") {
		t.Fatalf("Videos = %#v, want only the primary stream", result.Videos)
	}
}
