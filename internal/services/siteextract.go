package services

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/netguard"
)

// SiteVideo is one playable stream recovered from a webpage.
type SiteVideo struct {
	ID       string // site-side video id, when the page exposes one
	Title    string
	MediaURL string // direct m3u8/mp4 URL, ready for yt-dlp
	Format   string // "hls" or "mp4"
}

// SiteExtractResult is everything recovered from one page. A page can embed
// more than one player, so Videos is a list.
type SiteExtractResult struct {
	PageURL   string
	Title     string
	Thumbnail string
	Videos    []SiteVideo
}

const siteExtractMaxBody = 8 << 20 // 8 MiB

// Sentinel errors let the UI localize the common outcomes.
var (
	ErrEncryptedConfig = errors.New("page hands the player an encrypted config")
	ErrNoPlayableMedia = errors.New("no playable media URL on the page")
)

var (
	// Pages built on the shared DPlayer/DPanel plugin keep the real stream in
	// an inline player config such as:
	//   "video":{"url":"https:\/\/host\/path\/index.m3u8?auth_key=…","type":"hls"}
	// The slashes are JSON-escaped, which is why yt-dlp's generic extractor
	// never sees an https:// literal in the markup.
	siteVideoConfigRe = regexp.MustCompile(`(?s)"video"\s*:\s*\{[^{}]*?"url"\s*:\s*"([^"]+)"`)
	siteH265ConfigRe  = regexp.MustCompile(`(?s)"video_h265"\s*:\s*\{[^{}]*?"url"\s*:\s*"([^"]+)"`)
	siteEncryptedRe   = regexp.MustCompile(`encryptedConfig|encryptedData`)
	siteOGTitleRe     = regexp.MustCompile(`(?is)<meta[^>]+(?:property|name)=["']og:title["'][^>]+content=["']([^"']*)["']`)
	siteOGImageRe     = regexp.MustCompile(`(?is)<meta[^>]+(?:property|name)=["']og:image["'][^>]+content=["']([^"']*)["']`)
	siteTitleTagRe    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	sitePlayerDivRe   = regexp.MustCompile(`(?is)<div\b[^>]*\bclass\s*=\s*["']([^"']*)["'][^>]*>`)
	sitePlayerAttrRe  = regexp.MustCompile(`(?is)\bdata-video_(title|id)\s*=\s*["']([^"']*)["']`)
)

// unescapeJSString resolves the escapes used inside inline player configs:
// JSON's escaped solidus (\/) and JavaScript \uXXXX sequences. URLs must not
// keep either form, or yt-dlp will request the wrong host/path.
func unescapeJSString(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case '/':
				b.WriteByte('/')
				i++
				continue
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			case 'u':
				if i+5 < len(s) {
					if v, err := strconv.ParseUint(s[i+2:i+6], 16, 32); err == nil {
						b.WriteRune(rune(v))
						i += 5
						continue
					}
				}
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ExtractSiteMedia fetches pageURL and returns every stream embedded in its
// player configurations. It only handles pages that ship the streams in plain
// text inside an inline script; pages that encrypt the config are reported as
// unsupported.
func ExtractSiteMedia(ctx context.Context, pageURL string) (*SiteExtractResult, error) {
	return extractSiteMediaWithClient(ctx, pageURL, &http.Client{Timeout: 30 * time.Second})
}

// extractSiteMediaWithClient is the same fetch with an injectable client for
// tests. The client is always wrapped with the public-destination guard, so a
// redirect to a loopback, private, or link-local address is rejected.
func extractSiteMediaWithClient(ctx context.Context, pageURL string, client *http.Client) (*SiteExtractResult, error) {
	parsed, err := parseHTTPURL(pageURL)
	if err != nil {
		return nil, fmt.Errorf("invalid page URL: %w", err)
	}
	if !netguard.ValidURL(parsed) {
		return nil, errors.New("page URL is not public")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	requestClient := netguard.WrapClient(client)
	resp, err := requestClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// net/http errors can embed redirect targets and signed query values.
		return nil, errors.New("load page: request failed")
	}
	defer requestClient.CloseIdleConnections()
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("load page: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, siteExtractMaxBody))
	if err != nil {
		return nil, fmt.Errorf("read page: %w", err)
	}
	return parseSitePage(parsed.String(), string(body))
}

type sitePlayer struct {
	pos   int
	id    string
	title string
}

// parseSitePage finds every player stream URL in an already-fetched webpage.
func parseSitePage(pageURL, body string) (*SiteExtractResult, error) {
	primary := siteVideoConfigRe.FindAllStringSubmatchIndex(body, -1)
	h265 := siteH265ConfigRe.FindAllStringSubmatchIndex(body, -1)
	if len(primary) == 0 && len(h265) == 0 {
		if siteEncryptedRe.MatchString(body) {
			return nil, ErrEncryptedConfig
		}
		return nil, ErrNoPlayableMedia
	}

	result := &SiteExtractResult{
		PageURL:   pageURL,
		Title:     sitePageTitle(body),
		Thumbnail: sitePageImage(body),
		Videos:    siteVideos(body, primary, h265, sitePlayers(body)),
	}
	if len(result.Videos) == 0 {
		// Every config held an unusable URL.
		return nil, ErrNoPlayableMedia
	}
	return result, nil
}

// siteVideos collects the distinct streams from both config kinds, in document
// order, and labels each one with the title of the player element that
// precedes it. A player that ships both variants contributes only its primary
// stream; a player that ships only H.265 still contributes one.
func siteVideos(body string, primary, h265 [][]int, players []sitePlayer) []SiteVideo {
	type configMatch struct {
		idx  []int
		h265 bool
	}
	matches := make([]configMatch, 0, len(primary)+len(h265))
	for _, idx := range primary {
		matches = append(matches, configMatch{idx: idx})
	}
	for _, idx := range h265 {
		matches = append(matches, configMatch{idx: idx, h265: true})
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].idx[0] < matches[j].idx[0] })

	videos := make([]SiteVideo, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	seenPlayers := make(map[int]bool)
	for _, match := range matches {
		idx := match.idx
		raw := body[idx[2]:idx[3]]
		mediaURL := strings.TrimSpace(unescapeJSString(html.UnescapeString(raw)))
		parsed, err := parseHTTPURL(mediaURL)
		if err != nil || seen[mediaURL] {
			continue
		}
		seen[mediaURL] = true

		video := SiteVideo{MediaURL: mediaURL, Format: "mp4"}
		if strings.Contains(strings.ToLower(parsed.Path), ".m3u8") {
			video.Format = "hls"
		}
		if player := playerBefore(players, idx[0]); player != nil {
			if seenPlayers[player.pos] {
				continue
			}
			seenPlayers[player.pos] = true
			video.ID = player.id
			video.Title = player.title
		} else if len(primary) > 0 && match.h265 {
			// No player markup to pair with: keep the primary stream only.
			continue
		}
		videos = append(videos, video)
	}
	return videos
}

// sitePlayers records each player element's position and its data-video_*
// attributes. Only elements whose class list contains "dplayer" count, so the
// loading placeholder (class "dplayer-loading") is ignored.
func sitePlayers(body string) []sitePlayer {
	var players []sitePlayer
	for _, m := range sitePlayerDivRe.FindAllStringSubmatchIndex(body, -1) {
		classes := strings.Fields(body[m[2]:m[3]])
		isPlayer := false
		for _, class := range classes {
			if class == "dplayer" {
				isPlayer = true
				break
			}
		}
		if !isPlayer {
			continue
		}
		tag := body[m[0]:m[1]]
		player := sitePlayer{pos: m[0]}
		for _, attr := range sitePlayerAttrRe.FindAllStringSubmatch(tag, -1) {
			value := strings.TrimSpace(html.UnescapeString(attr[2]))
			switch strings.ToLower(attr[1]) {
			case "title":
				player.title = value
			case "id":
				player.id = value
			}
		}
		players = append(players, player)
	}
	return players
}

func playerBefore(players []sitePlayer, pos int) *sitePlayer {
	var found *sitePlayer
	for i := range players {
		if players[i].pos >= pos {
			break
		}
		found = &players[i]
	}
	return found
}

func sitePageTitle(body string) string {
	if m := siteOGTitleRe.FindStringSubmatch(body); m != nil {
		if title := strings.TrimSpace(html.UnescapeString(m[1])); title != "" {
			return title
		}
	}
	if m := siteTitleTagRe.FindStringSubmatch(body); m != nil {
		return strings.TrimSpace(html.UnescapeString(m[1]))
	}
	return ""
}

func sitePageImage(body string) string {
	if m := siteOGImageRe.FindStringSubmatch(body); m != nil {
		return strings.TrimSpace(html.UnescapeString(m[1]))
	}
	return ""
}
