# Semcrawl

Semcrawl is a Go command-line tool for browser-based scraping. It can load a URL
in headless Chrome or Chromium, keep browser sessions alive across commands, and
use the Jev API to identify page elements from natural-language descriptions and
extract fields from them.

The project also provides a one-shot `scrape` command for loading a page,
returning its rendered HTML, and closing the browser session without starting
the daemon.

## Requirements

- Go 1.24 or later
- Chrome or Chromium installed on the machine
- A TypeSafe API key for semantic extraction with Jev

## Build

```sh
go build -o semcrawl ./cmd/semcrawl
```

You can run commands with `./semcrawl`, or use `go run ./cmd/semcrawl` as in
the examples below.

## Quick start

### One-shot page load

This command starts a browser, loads the URL, prints the final URL, page title,
and rendered HTML as JSON, then closes the browser:

```sh
go run ./cmd/semcrawl scrape https://example.com
```

The default timeout is 60 seconds. It can be changed with `--timeout`:

```sh
go run ./cmd/semcrawl scrape --timeout 90s https://example.com
```

### Persistent session and semantic extraction

`open` starts the local daemon automatically if needed and creates a browser
session. The daemon reads `TYPESAFE_API_KEY` at startup, so set it before the
first `open` if you plan to use `extract`:

```sh
export TYPESAFE_API_KEY="your-api-key"

go run ./cmd/semcrawl open
```

`open` prints JSON containing a `session_id`. Use that ID in subsequent
commands:

```sh
SESSION_ID="paste-session-id-here"

go run ./cmd/semcrawl goto "$SESSION_ID" https://example.com
go run ./cmd/semcrawl content "$SESSION_ID"
go run ./cmd/semcrawl extract "$SESSION_ID" \
  --target "links to documentation pages" \
  --field label="the visible link text" \
  --field url="the destination URL" \
  --limit 20
go run ./cmd/semcrawl close "$SESSION_ID"
```

The `--target` value describes the records to find. Each `--field` is written
as `name=description`: the name becomes the JSON key, and the description tells
Jev what value to select. The response includes an `items` array; each item has
a `node_id`, a match `score`, and a `values` object. The response also reports
the Jev model and token usage. `--limit` sets the maximum number of matching
items returned (up to 100), after the page's usable DOM candidates have been
evaluated. One to twelve fields can be requested.

`extract` sends compact summaries of visible page elements to the TypeSafe Jev
API. Do not use it with page content that should not be sent to that service.

## Commands

| Command | Description |
| --- | --- |
| `semcrawl scrape [--timeout duration] <url>` | Load a URL, return rendered HTML, and close the browser. |
| `semcrawl open` | Start the daemon if needed and create a browser session. |
| `semcrawl goto <session_id> <url>` | Navigate an existing session to a URL. |
| `semcrawl content <session_id>` | Return the current page's final URL, title, and rendered HTML. |
| `semcrawl extract <session_id> --target <description> --field <name=description>... [--limit n]` | Find matching page items and extract requested fields using Jev. |
| `semcrawl close <session_id>` | Close a browser session. |
| `semcrawl session list` | List active sessions. |
| `semcrawl daemon start\|status\|stop` | Control or inspect the daemon. |

All commands print JSON to standard output. Errors are printed to standard
error.

## Daemon and sessions

The daemon uses a local Unix domain socket at `$XDG_RUNTIME_DIR/semcrawl.sock`.
If `XDG_RUNTIME_DIR` is unset, it uses
`<user config directory>/semcrawl/semcrawl.sock`. The socket is restricted to
the current user. Browser sessions expire after 10 minutes without activity;
the daemon exits after 15 minutes with no active sessions.

## Project layout

- `cmd/semcrawl/` — CLI commands and daemon entry point
- `internal/browser/` — headless browser and DOM access
- `internal/daemon/` — local daemon, HTTP API, and client
- `internal/semantic/` — Jev-backed DOM matching and field extraction
- `internal/scrape/` — one-shot URL loading and rendered HTML retrieval
- `jev/` — reusable Go client for the TypeSafe Jev API

See [`jev/README.md`](jev/README.md) for details on using the Jev client
library directly.
