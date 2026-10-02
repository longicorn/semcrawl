package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/longicorn/semcrawl/internal/browser"
	"github.com/longicorn/semcrawl/internal/semantic"
	"github.com/longicorn/semcrawl/internal/session"
	"github.com/longicorn/semcrawl/jev"
)

const (
	SessionIdleTimeout = 10 * time.Minute
	DaemonIdleTimeout  = 15 * time.Minute
)

type Server struct {
	manager     *session.Manager
	extractor   *semantic.Extractor
	factory     *browser.ChromeFactory
	mu          sync.Mutex
	lastRequest time.Time
	stopping    chan struct{}
	stopOnce    sync.Once
}

func NewServer(ctx context.Context, concurrency ...int) *Server {
	maxConcurrency := semantic.DefaultConcurrency
	if len(concurrency) > 0 {
		maxConcurrency = concurrency[0]
	}
	factory := browser.NewChromeFactory(ctx)
	return &Server{
		manager:     session.NewManager(factory),
		extractor:   semantic.NewExtractorWithConcurrency(jev.NewClient("", jev.WithMaxConcurrency(maxConcurrency)), maxConcurrency),
		factory:     factory,
		lastRequest: time.Now(),
		stopping:    make(chan struct{}),
	}
}

func Listen(socketPath string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0700); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}
	if _, err := os.Lstat(socketPath); err == nil {
		conn, dialErr := net.DialTimeout("unix", socketPath, 150*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return nil, errors.New("daemon is already listening")
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on daemon socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0600); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, fmt.Errorf("secure daemon socket: %w", err)
	}
	return listener, nil
}

func (s *Server) Serve(ctx context.Context, listener net.Listener, socketPath string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("POST /sessions", s.openSession)
	mux.HandleFunc("GET /sessions", s.listSessions)
	mux.HandleFunc("POST /sessions/{id}/goto", s.gotoPage)
	mux.HandleFunc("GET /sessions/{id}/content", s.content)
	mux.HandleFunc("POST /sessions/{id}/extract", s.extract)
	mux.HandleFunc("DELETE /sessions/{id}", s.closeSession)
	mux.HandleFunc("POST /daemon/stop", s.stopDaemon)
	httpServer := &http.Server{Handler: s.trackActivity(mux), ReadHeaderTimeout: 5 * time.Second}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.manager.CleanupIdle(SessionIdleTimeout)
				if s.manager.Count() == 0 && time.Since(s.getLastRequest()) >= DaemonIdleTimeout {
					s.requestStop()
					_ = httpServer.Shutdown(context.Background())
					return
				}
			case <-s.stopping:
				_ = httpServer.Shutdown(context.Background())
				return
			case <-ctx.Done():
				_ = httpServer.Shutdown(context.Background())
				return
			}
		}
	}()
	serveErr := httpServer.Serve(listener)
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		s.requestStop()
		<-shutdownDone
		return serveErr
	}
	s.requestStop()
	<-shutdownDone
	s.manager.CloseAll()
	_ = s.factory.Close()
	_ = os.Remove(socketPath)
	return nil
}

func (s *Server) trackActivity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.lastRequest = time.Now()
		s.mu.Unlock()
		defer func() {
			if recovered := recover(); recovered != nil {
				// Keep an unexpected handler panic from appearing to clients as a
				// bare EOF. Handlers normally write only after their work succeeds,
				// so this returns a useful error response for failures during work.
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error": fmt.Sprintf("daemon handler panic: %v", recovered),
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) getLastRequest() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRequest
}

func (s *Server) requestStop() {
	s.stopOnce.Do(func() { close(s.stopping) })
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "running", "sessions": s.manager.Count()})
}

func (s *Server) openSession(w http.ResponseWriter, r *http.Request) {
	info, err := s.manager.Open(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, info)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sessions": s.manager.List()})
}

func (s *Server) gotoPage(w http.ResponseWriter, r *http.Request) {
	var request struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid JSON request"))
		return
	}
	page, err := s.manager.Navigate(r.Context(), r.PathValue("id"), request.URL)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, session.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) content(w http.ResponseWriter, r *http.Request) {
	page, err := s.manager.Content(r.Context(), r.PathValue("id"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, session.ErrNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, session.ErrNoPage) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) extract(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Target     string           `json:"target"`
		AnchorText string           `json:"anchor_text"`
		Fields     []semantic.Field `json:"fields"`
		Limit      int              `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid JSON request"))
		return
	}
	if request.Limit == 0 {
		request.Limit = 20
	}
	targetForValidation := request.Target
	if strings.TrimSpace(targetForValidation) == "" && strings.TrimSpace(request.AnchorText) != "" {
		targetForValidation = "records containing the exact text " + strings.TrimSpace(request.AnchorText)
	}
	if err := semantic.Validate(targetForValidation, request.Fields, request.Limit); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	page, err := s.manager.DOM(r.Context(), r.PathValue("id"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, session.ErrNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, session.ErrNoPage) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	result, err := s.extractor.ExtractWithOptions(r.Context(), page, request.Target, request.Fields, request.Limit, semantic.ExtractOptions{AnchorText: request.AnchorText})
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, semantic.ErrNoCandidates) {
			status = http.StatusUnprocessableEntity
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) closeSession(w http.ResponseWriter, r *http.Request) {
	if err := s.manager.Close(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "closed"})
}

func (s *Server) stopDaemon(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.requestStop()
	}()
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
