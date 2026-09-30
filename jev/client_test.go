package jev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEvaluateSendsRequestAndDecodesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" {
			t.Errorf("request = %s %s, want POST /v1/systemone", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload["model"] != DefaultModel {
			t.Errorf("model = %v, want default %q", payload["model"], DefaultModel)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"is_ok":{"type":"noul","noul":0.91}},"usage":{"input_tokens":12,"output_tokens":2}}`))
	}))
	defer server.Close()

	client := NewClient("test-key", WithBaseURL(server.URL))
	got, err := client.Evaluate(context.Background(), Request{
		State: "example",
		Questions: map[string]Question{
			"is_ok": {Type: QuestionNoul, Instructions: "Is this okay?"},
		},
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if got.Model != "jev-1.13.0" || got.Usage.InputTokens != 12 {
		t.Fatalf("unexpected response: %+v", got)
	}
	answer := got.Answers["is_ok"]
	if answer.Type != QuestionNoul || answer.Noul == nil || *answer.Noul != 0.91 {
		t.Fatalf("unexpected answer: %+v", answer)
	}
}

func TestListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("request = %s %s, want GET /v1/models", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"jev-latest","description":"latest","release_date":"2026-09-15"}]}`))
	}))
	defer server.Close()

	got, err := NewClient("test-key", WithBaseURL(server.URL)).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].Name != "jev-latest" {
		t.Fatalf("unexpected models: %+v", got.Models)
	}
}

func TestAPIKeyCanComeFromEnvironment(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "environment-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer environment-key" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()

	if _, err := NewClient("", WithBaseURL(server.URL)).ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
}

func TestMissingAPIKeyAndQuestions(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	if _, err := NewClient("").Evaluate(context.Background(), Request{}); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("missing key error = %v", err)
	}
	if _, err := NewClient("key").Evaluate(context.Background(), Request{}); err == nil || !strings.Contains(err.Error(), "question") {
		t.Fatalf("missing questions error = %v", err)
	}
}

func TestAPIErrorPreservesStatusAndBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"invalid request"}`, http.StatusUnprocessableEntity)
	}))
	defer server.Close()

	_, err := NewClient("key", WithBaseURL(server.URL)).ListModels(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %T %v, want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(apiErr.Body), "invalid request") {
		t.Fatalf("unexpected API error: %+v", apiErr)
	}
}

func TestMalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	_, err := NewClient("key", WithBaseURL(server.URL)).ListModels(context.Background())
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("error = %v, want decode error", err)
	}
}

func TestContextCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewClient("key", WithBaseURL(server.URL)).ListModels(ctx)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestExplicitAPIKeyIgnoresEnvironment(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "environment-key")
	if got := NewClient("explicit-key").apiKey; got != "explicit-key" {
		t.Fatalf("apiKey = %q", got)
	}
}
