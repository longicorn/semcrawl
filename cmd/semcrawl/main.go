package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/longicorn/semcrawl/internal/daemon"
	"github.com/longicorn/semcrawl/internal/scrape"
	"github.com/longicorn/semcrawl/internal/semantic"
	"github.com/longicorn/semcrawl/internal/session"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "semcrawl:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "scrape":
		return runScrape(args[1:], stdout, stderr)
	case "open":
		if len(args) != 1 {
			return errors.New("usage: semcrawl open")
		}
		client, err := newDaemonClient()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if err := ensureDaemon(ctx, client); err != nil {
			return err
		}
		var opened session.Info
		if err := client.Call(ctx, http.MethodPost, "/sessions", map[string]any{}, &opened); err != nil {
			return err
		}
		return encode(stdout, opened)
	case "goto":
		if len(args) != 3 {
			return errors.New("usage: semcrawl goto <session_id> <url>")
		}
		client, err := newDaemonClient()
		if err != nil {
			return err
		}
		var page struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		}
		if err := client.Call(context.Background(), http.MethodPost, "/sessions/"+args[1]+"/goto", map[string]string{"url": args[2]}, &page); err != nil {
			return err
		}
		return encode(stdout, page)
	case "content":
		if len(args) != 2 {
			return errors.New("usage: semcrawl content <session_id>")
		}
		client, err := newDaemonClient()
		if err != nil {
			return err
		}
		var page map[string]any
		if err := client.Call(context.Background(), http.MethodGet, "/sessions/"+args[1]+"/content", nil, &page); err != nil {
			return err
		}
		return encode(stdout, page)
	case "extract":
		return runExtract(args[1:], stdout, stderr)
	case "close":
		if len(args) != 2 {
			return errors.New("usage: semcrawl close <session_id>")
		}
		client, err := newDaemonClient()
		if err != nil {
			return err
		}
		var result map[string]string
		if err := client.Call(context.Background(), http.MethodDelete, "/sessions/"+args[1], nil, &result); err != nil {
			return err
		}
		return encode(stdout, result)
	case "session":
		if len(args) != 2 || args[1] != "list" {
			return errors.New("usage: semcrawl session list")
		}
		client, err := newDaemonClient()
		if err != nil {
			return err
		}
		var result map[string]any
		if err := client.Call(context.Background(), http.MethodGet, "/sessions", nil, &result); err != nil {
			return err
		}
		return encode(stdout, result)
	case "daemon":
		return runDaemonCommand(args[1:], stdout)
	default:
		return usageError()
	}
}

type repeatedFlag []string

func (f *repeatedFlag) String() string { return strings.Join(*f, ",") }
func (f *repeatedFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func runExtract(args []string, stdout, stderr io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: semcrawl extract <session_id> --target <description> --field <name=description> [--field ...] [--limit n]")
	}
	sessionID := args[0]
	flags := flag.NewFlagSet("extract", flag.ContinueOnError)
	flags.SetOutput(stderr)
	target := flags.String("target", "", "natural language description of the elements to find")
	limit := flags.Int("limit", 20, "maximum number of matching items")
	var fields repeatedFlag
	flags.Var(&fields, "field", "output field as name=natural language description; repeat as needed")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments; use --target and --field")
	}
	parsedFields := make([]semantic.Field, 0, len(fields))
	for _, raw := range fields {
		name, description, ok := strings.Cut(raw, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(description) == "" {
			return fmt.Errorf("invalid field %q: expected name=description", raw)
		}
		parsedFields = append(parsedFields, semantic.Field{Name: strings.TrimSpace(name), Description: strings.TrimSpace(description)})
	}
	client, err := newDaemonClient()
	if err != nil {
		return err
	}
	var result map[string]any
	request := map[string]any{"target": *target, "fields": parsedFields, "limit": *limit}
	if err := client.Call(context.Background(), http.MethodPost, "/sessions/"+sessionID+"/extract", request, &result); err != nil {
		return err
	}
	return encode(stdout, result)
}

func runScrape(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("scrape", flag.ContinueOnError)
	flags.SetOutput(stderr)
	timeout := flags.Duration("timeout", 60*time.Second, "maximum time allowed for browser startup and page loading")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: semcrawl scrape [--timeout duration] <url>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	result, err := scrape.URL(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	return encode(stdout, result)
}

func runDaemonCommand(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: semcrawl daemon start|status|stop|run")
	}
	client, err := newDaemonClient()
	if err != nil {
		return err
	}
	switch args[0] {
	case "run":
		return runDaemon()
	case "start":
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := ensureDaemon(ctx, client); err != nil {
			return err
		}
		return encode(stdout, map[string]string{"status": "running"})
	case "status":
		status, err := client.Healthy(context.Background())
		if err != nil {
			return err
		}
		return encode(stdout, status)
	case "stop":
		var result map[string]string
		if err := client.Call(context.Background(), http.MethodPost, "/daemon/stop", map[string]any{}, &result); err != nil {
			return err
		}
		return encode(stdout, result)
	default:
		return errors.New("usage: semcrawl daemon start|status|stop")
	}
}

func runDaemon() error {
	socketPath, err := daemon.DefaultSocketPath()
	if err != nil {
		return err
	}
	listener, err := daemon.Listen(socketPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	server := daemon.NewServer(ctx)
	return server.Serve(ctx, listener, socketPath)
}

func newDaemonClient() (*daemon.Client, error) {
	socketPath, err := daemon.DefaultSocketPath()
	if err != nil {
		return nil, err
	}
	return daemon.NewClient(socketPath), nil
}

func ensureDaemon(ctx context.Context, client *daemon.Client) error {
	if _, err := client.Healthy(ctx); err == nil {
		return nil
	}
	startErr := daemon.StartProcess()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := client.Healthy(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			if startErr != nil {
				return fmt.Errorf("start daemon: %w", startErr)
			}
			return fmt.Errorf("wait for daemon startup: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func encode(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func usageError() error {
	return errors.New("usage: semcrawl open | goto <session_id> <url> | content <session_id> | extract <session_id> --target <description> --field <name=description> | close <session_id> | session list | daemon start|status|stop | scrape <url>")
}
