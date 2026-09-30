package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/longicorn/semcrawl/internal/browser"
)

var ErrNotFound = errors.New("session not found")
var ErrNoPage = errors.New("session has no page; call goto first")

type Info struct {
	ID         string    `json:"session_id"`
	Status     string    `json:"status"`
	URL        string    `json:"url,omitempty"`
	LastActive time.Time `json:"last_active"`
	IdleFor    string    `json:"idle_for"`
}

type entry struct {
	mu         sync.Mutex
	tab        browser.Tab
	url        string
	lastActive time.Time
	closed     bool
}

type Manager struct {
	mu       sync.Mutex
	factory  browser.Factory
	sessions map[string]*entry
}

func NewManager(factory browser.Factory) *Manager {
	return &Manager{factory: factory, sessions: make(map[string]*entry)}
}

func (m *Manager) Open(ctx context.Context) (Info, error) {
	tab, err := m.factory.Open(ctx)
	if err != nil {
		return Info{}, err
	}
	id, err := newID()
	if err != nil {
		_ = tab.Close()
		return Info{}, fmt.Errorf("create session id: %w", err)
	}
	now := time.Now()
	e := &entry{tab: tab, lastActive: now}
	m.mu.Lock()
	m.sessions[id] = e
	m.mu.Unlock()
	return info(id, e, now), nil
}

func (m *Manager) Navigate(ctx context.Context, id, rawURL string) (browser.Page, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return browser.Page{}, errors.New("URL must be an absolute http or https URL")
	}
	e, err := m.lookup(id)
	if err != nil {
		return browser.Page{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return browser.Page{}, ErrNotFound
	}
	page, err := e.tab.Navigate(ctx, parsed.String())
	if err != nil {
		return browser.Page{}, fmt.Errorf("navigate: %w", err)
	}
	e.url = page.URL
	e.lastActive = time.Now()
	return page, nil
}

func (m *Manager) Content(ctx context.Context, id string) (browser.Page, error) {
	e, err := m.lookup(id)
	if err != nil {
		return browser.Page{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return browser.Page{}, ErrNotFound
	}
	if e.url == "" {
		return browser.Page{}, ErrNoPage
	}
	page, err := e.tab.Snapshot(ctx)
	if err != nil {
		return browser.Page{}, fmt.Errorf("get page content: %w", err)
	}
	e.lastActive = time.Now()
	return page, nil
}

func (m *Manager) DOM(ctx context.Context, id string) (browser.DOMSnapshot, error) {
	e, err := m.lookup(id)
	if err != nil {
		return browser.DOMSnapshot{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return browser.DOMSnapshot{}, ErrNotFound
	}
	if e.url == "" {
		return browser.DOMSnapshot{}, ErrNoPage
	}
	snapshot, err := e.tab.DOMSnapshot(ctx)
	if err != nil {
		return browser.DOMSnapshot{}, fmt.Errorf("read page DOM: %w", err)
	}
	e.lastActive = time.Now()
	return snapshot, nil
}

func (m *Manager) Close(id string) error {
	m.mu.Lock()
	e, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	return e.tab.Close()
}

func (m *Manager) List() []Info {
	m.mu.Lock()
	items := make(map[string]*entry, len(m.sessions))
	for id, e := range m.sessions {
		items[id] = e
	}
	m.mu.Unlock()
	now := time.Now()
	out := make([]Info, 0, len(items))
	for id, e := range items {
		e.mu.Lock()
		out = append(out, info(id, e, now))
		e.mu.Unlock()
	}
	return out
}

func (m *Manager) CleanupIdle(idle time.Duration) int {
	cutoff := time.Now().Add(-idle)
	m.mu.Lock()
	var expired []string
	for id, e := range m.sessions {
		e.mu.Lock()
		old := e.lastActive.Before(cutoff)
		e.mu.Unlock()
		if old {
			expired = append(expired, id)
		}
	}
	m.mu.Unlock()
	closed := 0
	for _, id := range expired {
		if m.Close(id) == nil {
			closed++
		}
	}
	return closed
}

func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

func (m *Manager) CloseAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Close(id)
	}
}

func (m *Manager) lookup(id string) (*entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return e, nil
}

func info(id string, e *entry, now time.Time) Info {
	idle := now.Sub(e.lastActive)
	return Info{ID: id, Status: "active", URL: e.url, LastActive: e.lastActive, IdleFor: idle.Round(time.Second).String()}
}

func newID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
