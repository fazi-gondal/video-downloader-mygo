package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

const (
	aryPlusDefaultAPIBase = "https://be.aryplus.tv"
	aryPlusDefaultAPIKey  = "ak_37515186786306625012043079404171"
)

var (
	aryPlusEpisodeRe = regexp.MustCompile(`^https?://(?:www\.)?aryplus\.tv/video/v2/(?P<platformid>\d+)/(?P<videoid>[\w]+)(?:/(?P<seriesid>[\w]+))?/?(?:[?#].*)?$`)
	aryPlusSeriesRe  = regexp.MustCompile(`^https?://(?:(?:www\.)?aryplus\.tv/series|social\.aryplus\.tv/sharer/series)/(?P<id>[\w]+)/?(?:[?#].*)?$`)
)

// IsAryPlusURL reports whether rawURL is an ARY Plus episode or series URL.
func IsAryPlusURL(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	return aryPlusEpisodeRe.MatchString(rawURL) || aryPlusSeriesRe.MatchString(rawURL)
}

// IsAryPlusEpisodeURL reports whether rawURL is an ARY Plus episode video URL.
func IsAryPlusEpisodeURL(rawURL string) bool {
	return aryPlusEpisodeRe.MatchString(strings.TrimSpace(rawURL))
}

// IsAryPlusSeriesURL reports whether rawURL is an ARY Plus series playlist URL.
func IsAryPlusSeriesURL(rawURL string) bool {
	return aryPlusSeriesRe.MatchString(strings.TrimSpace(rawURL))
}

// AryPlusEpisodeMetadata holds resolved metadata for an ARY Plus episode.
type AryPlusEpisodeMetadata struct {
	VideoID     string
	PlatformID  string
	SeriesID    string
	Title       string
	Description string
	VideoSource string
	Thumbnail   string
	Duration    float64
	DrmEnabled  bool
	SeriesTitle string
}

func aryPlusAPIBase() string {
	if base := strings.TrimSpace(os.Getenv("ARYPLUS_API_BASE")); base != "" {
		return strings.TrimRight(base, "/")
	}
	return aryPlusDefaultAPIBase
}

func aryPlusAPIKey() string {
	if key := strings.TrimSpace(os.Getenv("ARYPLUS_API_KEY")); key != "" {
		return key
	}
	return aryPlusDefaultAPIKey
}

func aryPlusCallAPI(ctx context.Context, endpoint string, query url.Values) (map[string]any, error) {
	reqURL := aryPlusAPIBase() + "/" + strings.TrimLeft(endpoint, "/")
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create ARY Plus request: %w", err)
	}

	req.Header.Set("x-api-key", aryPlusAPIKey())
	req.Header.Set("Referer", "https://aryplus.tv/")
	req.Header.Set("Origin", "https://aryplus.tv")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call ARY Plus API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("ARY Plus API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode ARY Plus API response: %w", err)
	}
	return data, nil
}

// ResolveAryPlusEpisode resolves episode details, DRM checks, and video stream source URL.
func ResolveAryPlusEpisode(ctx context.Context, rawURL string) (*AryPlusEpisodeMetadata, error) {
	match := aryPlusEpisodeRe.FindStringSubmatch(strings.TrimSpace(rawURL))
	if len(match) == 0 {
		return nil, fmt.Errorf("invalid ARY Plus episode URL: %q", rawURL)
	}

	platformID := match[1]
	videoID := match[2]
	seriesID := ""
	if len(match) > 3 {
		seriesID = match[3]
	}

	res := &AryPlusEpisodeMetadata{
		VideoID:    videoID,
		PlatformID: platformID,
		SeriesID:   seriesID,
	}

	// Platform 1 = YouTube, Platform 2 = Dailymotion
	if platformID == "1" || platformID == "2" {
		return res, nil
	}
	if platformID != "3" {
		return nil, fmt.Errorf("unsupported ARY Plus platform ID: %s", platformID)
	}

	data, err := aryPlusCallAPI(ctx, fmt.Sprintf("api/cdn/ep/%s", videoID), nil)
	if err != nil {
		return nil, err
	}

	epMap := extractEpisodeObject(data["episode"])
	if epMap == nil {
		return nil, fmt.Errorf("episode data not found for ID: %s", videoID)
	}

	if drm, _ := epMap["drmEnabled"].(bool); drm {
		return nil, errors.New("this video is DRM protected and cannot be downloaded")
	}

	videoSource, _ := epMap["videoSource"].(string)
	if strings.TrimSpace(videoSource) == "" {
		return nil, errors.New("no video source stream found in ARY Plus response")
	}
	res.VideoSource = strings.TrimSpace(videoSource)
	res.Description, _ = epMap["description"].(string)

	if seriesID == "" {
		if sid, _ := epMap["seriesId"].(string); sid != "" {
			seriesID = sid
			res.SeriesID = sid
		}
	}

	var seriesTitle string
	if seriesID != "" {
		if sData, err := aryPlusCallAPI(ctx, fmt.Sprintf("api/series/%s", seriesID), nil); err == nil && sData != nil {
			seriesTitle, _ = sData["title"].(string)
			res.SeriesTitle = seriesTitle
		}
	}

	epTitle, _ := epMap["title"].(string)
	if epTitle == "" {
		if num, ok := epMap["videoEpNumber"]; ok {
			epTitle = fmt.Sprintf("Episode %v", num)
		} else {
			epTitle = fmt.Sprintf("Episode %s", videoID)
		}
	}

	if seriesTitle != "" {
		res.Title = fmt.Sprintf("%s - %s", seriesTitle, epTitle)
	} else {
		res.Title = epTitle
	}

	imagePath, _ := epMap["imagePathV2"].(string)
	if imagePath == "" {
		imagePath, _ = epMap["imagePath"].(string)
	}
	if imagePath != "" {
		res.Thumbnail = "https://images.aryplus.tv/" + strings.TrimLeft(imagePath, "/")
	}

	if lengthVal, ok := epMap["videoLength"]; ok {
		res.Duration = parseDurationValue(lengthVal)
	}

	return res, nil
}

// FetchAryPlusSeriesPlaylist retrieves all episodes of an ARY Plus series as a PlaylistInfo.
func FetchAryPlusSeriesPlaylist(ctx context.Context, rawURL string) (*models.PlaylistInfo, error) {
	match := aryPlusSeriesRe.FindStringSubmatch(strings.TrimSpace(rawURL))
	if len(match) == 0 {
		return nil, fmt.Errorf("invalid ARY Plus series URL: %q", rawURL)
	}

	seriesID := match[1]

	var seriesTitle string
	if sData, err := aryPlusCallAPI(ctx, fmt.Sprintf("api/series/%s", seriesID), nil); err == nil && sData != nil {
		seriesTitle, _ = sData["title"].(string)
	}
	if seriesTitle == "" {
		seriesTitle = seriesID
	}

	playlist := &models.PlaylistInfo{
		ID:          seriesID,
		Title:       seriesTitle,
		OriginalURL: rawURL,
		WebpageURL:  rawURL,
	}

	page := 1
	for {
		query := url.Values{}
		query.Set("page", strconv.Itoa(page))
		query.Set("limit", "50")

		data, err := aryPlusCallAPI(ctx, fmt.Sprintf("api/v2/cdn/pg/%s", seriesID), query)
		if err != nil {
			if len(playlist.Entries) > 0 {
				break
			}
			return nil, err
		}

		rawEpisodes, ok := data["episode"].([]any)
		if !ok || len(rawEpisodes) == 0 {
			break
		}

		for _, rawEp := range rawEpisodes {
			epMap, ok := rawEp.(map[string]any)
			if !ok {
				continue
			}
			epID, _ := epMap["_id"].(string)
			if epID == "" {
				continue
			}

			epTitle, _ := epMap["title"].(string)
			if epTitle == "" {
				if num, ok := epMap["videoEpNumber"]; ok {
					epTitle = fmt.Sprintf("Episode %v", num)
				} else {
					epTitle = fmt.Sprintf("Episode %s", epID)
				}
			}

			entryTitle := epTitle
			if seriesTitle != "" && seriesTitle != seriesID {
				entryTitle = fmt.Sprintf("%s - %s", seriesTitle, epTitle)
			}

			imagePath, _ := epMap["imagePathV2"].(string)
			if imagePath == "" {
				imagePath, _ = epMap["imagePath"].(string)
			}
			thumbnail := ""
			if imagePath != "" {
				thumbnail = "https://images.aryplus.tv/" + strings.TrimLeft(imagePath, "/")
			}

			entry := models.PlaylistEntry{
				Index:     len(playlist.Entries) + 1,
				ID:        epID,
				Title:     entryTitle,
				URL:       fmt.Sprintf("https://aryplus.tv/video/v2/3/%s/%s", epID, seriesID),
				Thumbnail: thumbnail,
				Duration:  parseDurationValue(epMap["videoLength"]),
			}
			playlist.Entries = append(playlist.Entries, entry)
		}

		totalPages := 1
		if tp, ok := data["totalPages"].(float64); ok && tp > 0 {
			totalPages = int(tp)
		}
		if page >= totalPages {
			break
		}
		page++
	}

	return playlist, nil
}

func extractEpisodeObject(raw any) map[string]any {
	switch v := raw.(type) {
	case map[string]any:
		return v
	case []any:
		if len(v) > 0 {
			if first, ok := v[0].(map[string]any); ok {
				return first
			}
		}
	}
	return nil
}

func parseDurationValue(val any) float64 {
	switch v := val.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return 0
}

func sanitizeFilename(name string) string {
	invalid := regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)
	cleaned := invalid.ReplaceAllString(name, "_")
	cleaned = strings.Trim(cleaned, ". \t\r\n")
	if cleaned == "" {
		return "video"
	}
	return cleaned
}
