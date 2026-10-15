package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFetchThumbnailPNG(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name          string
		width, height int
		wantW, wantH  int
	}{
		{"small", 32, 18, 32, 18},
		{"landscape", 1280, 720, 640, 360},
		{"portrait", 200, 1000, 200, 1000},
		{"thin", 1000, 1, 640, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data := thumbnailTestPNG(t, tt.width, tt.height)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(data)
			}))
			defer server.Close()
			bitmap, err := fetchThumbnail(t.Context(), thumbnailTestClient(server), thumbnailTestURL(server))
			if err != nil {
				t.Fatal(err)
			}
			if w, h := bitmap.Size(); w != tt.wantW || h != tt.wantH {
				t.Fatalf("size = %dx%d, want %dx%d", w, h, tt.wantW, tt.wantH)
			}
		})
	}
}

func TestFetchThumbnailRejectsURL(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, url string }{
		{"empty", ""},
		{"malformed", "http://[invalid"},
		{"relative", "/image.png"},
		{"no host", "https:///image.png"},
		{"no hostname", "http://:80/image.png"},
		{"file", "file:///image.png"},
		{"ftp", "ftp://example.com/image.png"},
		{"data", "data:image/png;base64,abc"},
		{"loopback", "http://127.0.0.1/image.png"},
		{"private", "http://192.168.1.1/image.png"},
		{"link local", "http://169.254.1.1/image.png"},
		{"reserved", "http://192.0.2.1/image.png"},
		{"localhost", "http://localhost/image.png"},
		{"ipv6 loopback", "http://[::1]/image.png"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bitmap, err := fetchThumbnail(t.Context(), nil, tt.url)
			if err == nil || bitmap != nil {
				t.Fatalf("got bitmap %v, error %v; want failure", bitmap, err)
			}
		})
	}
}

func TestFetchThumbnailRejectsResponse(t *testing.T) {
	t.Parallel()
	oversizedDimensions := thumbnailTestPNG(t, 1, 1)
	// A valid PNG IHDR with an excessive pixel count must fail before decoding.
	binary.BigEndian.PutUint32(oversizedDimensions[16:20], 100000)
	binary.BigEndian.PutUint32(oversizedDimensions[20:24], 100000)
	binary.BigEndian.PutUint32(oversizedDimensions[29:33], crc32.ChecksumIEEE(oversizedDimensions[12:29]))
	for _, tt := range []struct {
		name   string
		status int
		body   []byte
		chunk  bool
		want   string
	}{
		{"not found", 404, []byte("secret error page"), false, "HTTP status 404"},
		{"server error", 500, nil, false, "HTTP status 500"},
		{"empty", 200, nil, false, "invalid thumbnail image"},
		{"not image", 200, []byte("not an image"), false, "invalid thumbnail image"},
		{"pixel bomb", 200, oversizedDimensions, false, "pixel limit"},
		{"oversized body", 200, make([]byte, maxThumbnailBytes+1), true, "byte limit"},
		{"truncated image", 200, thumbnailTestPNG(t, 1, 1)[:33], false, "decoding thumbnail failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				if tt.chunk {
					w.(http.Flusher).Flush()
				}
				_, _ = w.Write(tt.body)
			}))
			defer server.Close()
			bitmap, err := fetchThumbnail(t.Context(), thumbnailTestClient(server), thumbnailTestURL(server))
			if bitmap != nil || err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got bitmap %v, error %v; want %q", bitmap, err, tt.want)
			}
		})
	}
}

func TestFetchThumbnailCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		cancel()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := thumbnailTestClient(server)
	client.Timeout = 5 * time.Second
	bitmap, err := fetchThumbnail(ctx, client, thumbnailTestURL(server))
	if bitmap != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("got bitmap %v, error %v; want context.Canceled", bitmap, err)
	}
}

func TestFetchThumbnailRedirectAndPrivacy(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "file:///secret", http.StatusFound)
	}))
	defer server.Close()
	bitmap, err := fetchThumbnail(t.Context(), thumbnailTestClient(server), thumbnailTestURL(server)+"?token=secret")
	if bitmap != nil || err == nil {
		t.Fatalf("got bitmap %v, error %v; want failure", bitmap, err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("error includes request details: %v", err)
	}
}

func TestFetchThumbnailRejectsPrivateRedirect(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1/secret", http.StatusFound)
	}))
	defer server.Close()

	bitmap, err := fetchThumbnail(t.Context(), thumbnailTestClient(server), thumbnailTestURL(server))
	if bitmap != nil || err == nil {
		t.Fatalf("got bitmap %v, error %v; want blocked redirect", bitmap, err)
	}
}

func thumbnailTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func thumbnailTestURL(server *httptest.Server) string {
	return "http://93.184.216.34/thumbnail.png"
}

func thumbnailTestClient(server *httptest.Server) *http.Client {
	target, err := url.Parse(server.URL)
	if err != nil {
		panic(err)
	}
	base := server.Client().Transport
	return &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		redirected := req.Clone(req.Context())
		targetCopy := *target
		targetCopy.Path = req.URL.Path
		targetCopy.RawQuery = req.URL.RawQuery
		redirected.URL = &targetCopy
		redirected.Host = targetCopy.Host
		return base.RoundTrip(redirected)
	})}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
