package scrape

import (
	"strings"
	"testing"
)

func TestParseURLAcceptsHTTPAndHTTPS(t *testing.T) {
	for _, raw := range []string{"https://example.com/path?q=1", "http://localhost:8080"} {
		if _, err := parseURL(raw); err != nil {
			t.Errorf("parseURL(%q) error = %v", raw, err)
		}
	}
}

func TestParseURLRejectsUnsupportedOrRelativeURLs(t *testing.T) {
	for _, raw := range []string{"", "example.com", "file:///tmp/page.html", "javascript:alert(1)", "ftp://example.com"} {
		if _, err := parseURL(raw); err == nil {
			t.Errorf("parseURL(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestNewSessionIDIsUUIDv4(t *testing.T) {
	value, err := newSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if len(value) != 36 || value[14] != '4' || !strings.Contains("89ab", string(value[19])) {
		t.Fatalf("session id %q is not a UUID v4", value)
	}
}
