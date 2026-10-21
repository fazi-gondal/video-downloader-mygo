package models

import "strings"

// FormatInfo describes one available stream from yt-dlp.
type FormatInfo struct {
	ID             string
	Ext            string
	Resolution     string
	FPS            float64
	VCodec         string
	ACodec         string
	Filesize       int64
	FilesizeApprox int64
	TBR            float64 // total bitrate kbps
	ABR            float64
	VBR            float64
	Language       string
	LanguagePref   int
	Note           string
	Protocol       string
	IsVideo        bool
	IsAudio        bool
	HasAudio       bool
	Height         int
	Width          int
	IsOriginal     bool
}

// AudioTrack is a selectable audio stream (for multi-audio).
type AudioTrack struct {
	ID         string
	Language   string
	Codec      string
	Bitrate    float64
	Note       string
	IsOriginal bool
}

// IsOriginalTrack returns true if the track is marked as the original audio stream.
func (t AudioTrack) IsOriginalTrack() bool {
	if t.IsOriginal {
		return true
	}
	note := strings.ToLower(t.Note)
	if strings.Contains(note, "original") {
		return true
	}
	lang := strings.ToLower(t.Language)
	if strings.Contains(lang, "orig") {
		return true
	}
	id := strings.ToLower(t.ID)
	if strings.Contains(id, "orig") {
		return true
	}
	if strings.Contains(note, "default") && !strings.Contains(note, "dubbed") {
		return true
	}
	return false
}

// SubtitleTrack is a selectable subtitle.
type SubtitleTrack struct {
	Lang string
	Name string
	Ext  string
	URL  string
}

// MediaInfo is the result of analyzing a single video URL.
type MediaInfo struct {
	ID                string
	Title             string
	Description       string
	Uploader          string
	Duration          float64 // seconds
	Thumbnail         string
	WebpageURL        string
	Extractor         string
	Formats           []FormatInfo
	AudioTracks       []AudioTrack
	Subtitles         map[string][]SubtitleTrack // lang -> tracks
	AutomaticCaptions map[string][]SubtitleTrack
	Is360             bool // source exposes YouTube's spherical/mesh video formats
	IsLive            bool
	OriginalURL       string
}

// PlaylistEntry is one item inside a playlist.
type PlaylistEntry struct {
	ID        string
	Title     string
	URL       string
	Duration  float64
	Thumbnail string
	Index     int
}

// PlaylistInfo is the result of analyzing a playlist/channel URL.
type PlaylistInfo struct {
	ID          string
	Title       string
	Uploader    string
	WebpageURL  string
	Entries     []PlaylistEntry
	OriginalURL string
}
