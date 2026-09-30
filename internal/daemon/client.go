package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const maxResponseBytes = 256 << 20

type Client struct {
	socketPath string
	http       *http.Client
}

func NewClient(socketPath string) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socketPath)
	}}
	return &Client{
		socketPath: socketPath,
		http:       &http.Client{Transport: transport, Timeout: 5 * time.Minute},
	}
}

func DefaultSocketPath() (string, error) {
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "semcrawl.sock"), nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(configDir, "semcrawl", "semcrawl.sock"), nil
}

func (c *Client) Call(ctx context.Context, method, path string, requestBody, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("encode daemon request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://semcrawl"+path, body)
	if err != nil {
		return err
	}
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("connect to semcrawl daemon (%s): %w", c.socketPath, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read daemon response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("daemon response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var payload struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &payload) == nil && payload.Error != "" {
			return fmt.Errorf("daemon: %s", payload.Error)
		}
		return fmt.Errorf("daemon returned HTTP %d: %s", resp.StatusCode, data)
	}
	if responseBody == nil {
		return nil
	}
	if err := json.Unmarshal(data, responseBody); err != nil {
		return fmt.Errorf("decode daemon response: %w", err)
	}
	return nil
}

func (c *Client) Healthy(ctx context.Context) (map[string]any, error) {
	var result map[string]any
	err := c.Call(ctx, http.MethodGet, "/health", nil, &result)
	return result, err
}
