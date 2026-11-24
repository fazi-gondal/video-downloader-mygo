package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"

	"github.com/fazi-gondal/video-downloader-mygo/internal/netguard"
)

const (
	maxThumbnailBytes  = 8 << 20
	maxThumbnailPixels = 16 << 20
	maxThumbnailWidth  = 640
)

// fetchThumbnail loads a bounded image without retaining request or URL state.
func fetchThumbnail(ctx context.Context, client *http.Client, rawURL string) (*ui.Bitmap, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || !netguard.ValidURL(parsed) {
		return nil, errors.New("invalid thumbnail URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, errors.New("invalid thumbnail request")
	}
	// Preserve the caller's timeout and settings while pinning DNS and
	// rechecking redirect targets.
	requestClient := netguard.WrapClient(client)
	resp, err := requestClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// net/http errors can embed signed URLs or credentials.
		return nil, errors.New("thumbnail request failed")
	}
	defer requestClient.CloseIdleConnections()
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("thumbnail HTTP status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxThumbnailBytes {
		return nil, errors.New("thumbnail exceeds byte limit")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxThumbnailBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("reading thumbnail failed")
	}
	if len(data) > maxThumbnailBytes {
		return nil, errors.New("thumbnail exceeds byte limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("invalid thumbnail image")
	}
	// Division avoids overflowing on attacker-controlled dimensions.
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxThumbnailPixels/config.Height {
		return nil, errors.New("thumbnail exceeds pixel limit")
	}
	if config.Width > maxThumbnailWidth {
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, errors.New("decoding thumbnail failed")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		width := maxThumbnailWidth
		height := max(1, int(int64(config.Height)*maxThumbnailWidth/int64(config.Width)))
		resized := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.ApproxBiLinear.Scale(resized, resized.Bounds(), img, img.Bounds(), draw.Src, nil)
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, resized); err != nil {
			return nil, errors.New("encoding thumbnail failed")
		}
		data = encoded.Bytes()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bitmap, err := ui.DecodeBitmap(data)
	if err != nil {
		return nil, errors.New("decoding thumbnail failed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return bitmap, nil
}
