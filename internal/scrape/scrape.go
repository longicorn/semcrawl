// Package scrape opens a short-lived Chrome session to retrieve rendered pages.
package scrape

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/chromedp/chromedp"
)

// Result contains the rendered page document and its final browser URL.
type Result struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	HTML      string `json:"html"`
}

type snapshot struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	HTML  string `json:"html"`
}

// URL loads a page in a new headless Chrome session, captures its rendered
// HTML, and closes the session before returning.
func URL(ctx context.Context, rawURL string) (*Result, error) {
	parsed, err := parseURL(rawURL)
	if err != nil {
		return nil, err
	}
	sessionID, err := newSessionID()
	if err != nil {
		return nil, fmt.Errorf("create session id: %w", err)
	}

	allocatorCtx, cancelAllocator := chromedp.NewExecAllocator(ctx, chromedp.DefaultExecAllocatorOptions[:]...)
	defer cancelAllocator()
	browserCtx, cancelBrowser := chromedp.NewContext(allocatorCtx)
	defer cancelBrowser()

	var page snapshot
	if err := chromedp.Run(browserCtx,
		chromedp.Navigate(parsed.String()),
		chromedp.Evaluate(`({url: location.href, title: document.title, html: document.documentElement ? document.documentElement.outerHTML : ""})`, &page),
	); err != nil {
		return nil, fmt.Errorf("open page %q: %w (install Chrome or Chromium if no browser is available)", parsed.String(), err)
	}
	if err := chromedp.Cancel(browserCtx); err != nil {
		return nil, fmt.Errorf("close browser session: %w", err)
	}

	return &Result{
		SessionID: sessionID,
		Status:    "closed",
		URL:       page.URL,
		Title:     page.Title,
		HTML:      page.HTML,
	}, nil
}

func parseURL(raw string) (*url.URL, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("URL must be an absolute http or https URL")
	}
	return parsed, nil
}

func newSessionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	// UUID v4 layout, encoded without external dependencies.
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
