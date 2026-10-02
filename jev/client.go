// Package jev provides a small Go client for the TypeSafe Jev API.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	DefaultBaseURL     = "https://api.typesafe.ai"
	DefaultModel       = "jev-latest"
	maxBodyBytes       = 8 << 20
	defaultConcurrency = 2
	maxConcurrency     = 8
)

// QuestionType identifies one of Jev's supported question types.
type QuestionType string

const (
	QuestionNoul   QuestionType = "noul"
	QuestionChoice QuestionType = "choice"
	QuestionScore  QuestionType = "score"
)

// Question describes a named decision to make about the request state.
// Criteria accepts the shape required by the selected question type.
type Question struct {
	Type         QuestionType `json:"type"`
	Instructions any          `json:"instructions,omitempty"`
	Criteria     any          `json:"criteria,omitempty"`
}

// Request is a System One request. State may be plain text or JSON-compatible
// structured data. Model defaults to jev-latest when omitted.
type Request struct {
	State     any                 `json:"state"`
	Model     string              `json:"model,omitempty"`
	Questions map[string]Question `json:"questions"`
}

// Answer contains the answer fields returned for any Jev question type.
// Only fields corresponding to Type are populated.
type Answer struct {
	Type          QuestionType       `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Legend        map[string]any     `json:"legend,omitempty"`
}

// Response is the result of a System One request.
type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// Usage reports the token usage returned by Jev.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Model describes a model or alias available to the authenticated account.
type Model struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReleaseDate string `json:"release_date"`
}

// ModelsResponse is the response from GET /v1/models.
type ModelsResponse struct {
	Models []Model `json:"models"`
}

// APIError represents a non-success HTTP response from the Jev API.
type APIError struct {
	StatusCode int
	Body       []byte
}

func (e *APIError) Error() string {
	message := strings.TrimSpace(string(e.Body))
	if message == "" {
		return fmt.Sprintf("jev: API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("jev: API returned HTTP %d: %s", e.StatusCode, message)
}

// Client calls the TypeSafe Jev API.
type Client struct {
	baseURL     string
	apiKey      string
	httpClient  *http.Client
	requests    chan struct{}
	concurrency int
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API base URL. It is primarily useful for proxies
// and tests; the default is https://api.typesafe.ai.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(baseURL, "/") }
}

// WithHTTPClient overrides the HTTP client. The caller retains control of the
// transport and timeout.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

// WithMaxConcurrency sets the maximum number of concurrent evaluation calls.
// Values below one use the default of two; values above eight are capped.
func WithMaxConcurrency(concurrency int) Option {
	return func(c *Client) {
		if concurrency < 1 {
			c.concurrency = defaultConcurrency
		} else if concurrency > maxConcurrency {
			c.concurrency = maxConcurrency
		} else {
			c.concurrency = concurrency
		}
	}
}

// NewClient creates a client. An empty apiKey reads TYPESAFE_API_KEY from the
// environment. The default HTTP timeout is 30 seconds.
func NewClient(apiKey string, options ...Option) *Client {
	if apiKey == "" {
		apiKey = os.Getenv("TYPESAFE_API_KEY")
	}
	c := &Client{
		baseURL:     DefaultBaseURL,
		apiKey:      apiKey,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		concurrency: defaultConcurrency,
	}
	for _, option := range options {
		option(c)
	}
	c.requests = make(chan struct{}, c.concurrency)
	return c
}

// Evaluate submits one or more named questions to POST /v1/systemone.
func (c *Client) Evaluate(ctx context.Context, request Request) (*Response, error) {
	if c.apiKey == "" {
		return nil, errors.New("jev: API key is required (pass it to NewClient or set TYPESAFE_API_KEY)")
	}
	if request.Model == "" {
		request.Model = DefaultModel
	}
	if len(request.Questions) == 0 {
		return nil, errors.New("jev: at least one question is required")
	}
	select {
	case c.requests <- struct{}{}:
		defer func() { <-c.requests }()
	case <-ctx.Done():
		return nil, fmt.Errorf("jev: wait for request slot: %w", ctx.Err())
	}
	return doJSON[Response](ctx, c, http.MethodPost, "/v1/systemone", request)
}

// ListModels returns models available to the authenticated account.
func (c *Client) ListModels(ctx context.Context) (*ModelsResponse, error) {
	if c.apiKey == "" {
		return nil, errors.New("jev: API key is required (pass it to NewClient or set TYPESAFE_API_KEY)")
	}
	return doJSON[ModelsResponse](ctx, c, http.MethodGet, "/v1/models", nil)
}

func doJSON[T any](ctx context.Context, c *Client, method, path string, body any) (*T, error) {
	var requestBody io.Reader
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("jev: encode request: %w", err)
		}
	}
	const maxRetries = 3
	for attempt := 0; ; attempt++ {
		if body != nil {
			requestBody = bytes.NewReader(encoded)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, requestBody)
		if err != nil {
			return nil, fmt.Errorf("jev: create request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("jev: request: %w", err)
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
		statusCode := resp.StatusCode
		retryAfter := resp.Header.Get("Retry-After")
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("jev: read response: %w", readErr)
		}
		if len(responseBody) > maxBodyBytes {
			return nil, fmt.Errorf("jev: response exceeds %d bytes", maxBodyBytes)
		}
		if statusCode == http.StatusTooManyRequests || statusCode == 529 {
			if attempt < maxRetries {
				delay := time.Duration(1<<attempt) * 250 * time.Millisecond
				if seconds, err := time.ParseDuration(retryAfter + "s"); err == nil && seconds > 0 {
					delay = seconds
				} else if retryTime, err := http.ParseTime(retryAfter); err == nil && time.Until(retryTime) > 0 {
					delay = time.Until(retryTime)
				}
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, fmt.Errorf("jev: retry wait: %w", ctx.Err())
				case <-timer.C:
				}
				continue
			}
		}
		if statusCode < 200 || statusCode >= 300 {
			return nil, &APIError{StatusCode: statusCode, Body: responseBody}
		}
		var result T
		if err := json.Unmarshal(responseBody, &result); err != nil {
			return nil, fmt.Errorf("jev: decode response: %w", err)
		}
		return &result, nil
	}
}
