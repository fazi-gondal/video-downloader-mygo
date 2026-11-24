package services

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// parseHTTPURL validates that raw is an absolute http(s) URL with a host.
func parseHTTPURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("URL is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return nil, errors.New("URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return nil, errors.New("URL must include a host")
	}
	return parsed, nil
}
