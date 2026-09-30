package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/longicorn/semcrawl/internal/browser"
)

type fakeFactory struct {
	mu   sync.Mutex
	tabs []*fakeTab
}

func (f *fakeFactory) Open(context.Context) (browser.Tab, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tab := &fakeTab{}
	f.tabs = append(f.tabs, tab)
	return tab, nil
}

func (f *fakeFactory) Close() error { return nil }

type fakeTab struct {
	mu     sync.Mutex
	page   browser.Page
	closed bool
}

func (t *fakeTab) Navigate(_ context.Context, rawURL string) (browser.Page, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.page = browser.Page{URL: rawURL, Title: "fixture", HTML: "<html>fixture</html>"}
	return t.page, nil
}

func (t *fakeTab) Snapshot(context.Context) (browser.Page, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.page, nil
}

func (t *fakeTab) DOMSnapshot(context.Context) (browser.DOMSnapshot, error) {
	return browser.DOMSnapshot{URL: t.page.URL, Title: t.page.Title, Nodes: []browser.DOMNode{}}, nil
}

func (t *fakeTab) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	return nil
}

func TestSessionLifecycle(t *testing.T) {
	factory := &fakeFactory{}
	manager := NewManager(factory)
	opened, err := manager.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if opened.Status != "active" || opened.ID == "" {
		t.Fatalf("unexpected session: %+v", opened)
	}
	if _, err := manager.Content(context.Background(), opened.ID); !errors.Is(err, ErrNoPage) {
		t.Fatalf("Content before goto error = %v, want ErrNoPage", err)
	}
	page, err := manager.Navigate(context.Background(), opened.ID, "https://example.com")
	if err != nil || page.Title != "fixture" {
		t.Fatalf("Navigate() = %+v, %v", page, err)
	}
	page, err = manager.Content(context.Background(), opened.ID)
	if err != nil || page.HTML != "<html>fixture</html>" {
		t.Fatalf("Content() = %+v, %v", page, err)
	}
	dom, err := manager.DOM(context.Background(), opened.ID)
	if err != nil || dom.URL != "https://example.com" {
		t.Fatalf("DOM() = %+v, %v", dom, err)
	}
	if got := len(manager.List()); got != 1 {
		t.Fatalf("List() has %d sessions, want 1", got)
	}
	if err := manager.Close(opened.ID); err != nil {
		t.Fatal(err)
	}
	if !factory.tabs[0].closed {
		t.Fatal("tab was not closed")
	}
	if err := manager.Close(opened.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Close() error = %v, want ErrNotFound", err)
	}
}

func TestNavigateRejectsInvalidURLs(t *testing.T) {
	manager := NewManager(&fakeFactory{})
	opened, err := manager.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(opened.ID)
	if _, err := manager.Navigate(context.Background(), opened.ID, "file:///etc/passwd"); err == nil || !strings.Contains(err.Error(), "http or https") {
		t.Fatalf("Navigate invalid URL error = %v", err)
	}
}

func TestCleanupIdleClosesExpiredSessions(t *testing.T) {
	factory := &fakeFactory{}
	manager := NewManager(factory)
	opened, err := manager.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := manager.lookup(opened.ID)
	entry.mu.Lock()
	entry.lastActive = time.Now().Add(-time.Hour)
	entry.mu.Unlock()
	if count := manager.CleanupIdle(time.Minute); count != 1 {
		t.Fatalf("CleanupIdle() = %d, want 1", count)
	}
	if !factory.tabs[0].closed || manager.Count() != 0 {
		t.Fatal("expired session was not removed and closed")
	}
}
