package netguard

import (
	"net/url"
	"testing"
)

func TestValidURL(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"https://example.com/page", true},
		{"http://93.184.216.34/page", true},
		{"http://127.0.0.1/page", false},
		{"http://10.0.0.1/page", false},
		{"http://192.168.1.1/page", false},
		{"http://169.254.1.1/page", false},
		{"http://192.0.2.1/page", false},
		{"http://[::1]/page", false},
		{"http://localhost/page", false},
		{"http://printer.local/page", false},
		{"http://user:pass@example.com/page", false},
		{"file:///tmp/page", false},
		{"ftp://example.com/page", false},
		{"data:text/html,hello", false},
	}
	for _, tt := range tests {
		u, err := url.Parse(tt.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tt.raw, err)
		}
		if got := ValidURL(u); got != tt.want {
			t.Errorf("ValidURL(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}
