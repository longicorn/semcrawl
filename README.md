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
export SEMCRAWL_JEV_CONCURRENCY=2

go run ./cmd/semcrawl open
```

`SEMCRAWL_JEV_CONCURRENCY` sets concurrent Jev evaluations per daemon. It
defaults to `2` and accepts values from `1` to `8`. Restart the daemon after
changing it.

`open` prints JSON containing a `session_id`. Use that ID in subsequent
commands:

```sh
SESSION_ID="paste-session-id-here"

go run ./cmd/semcrawl goto "$SESSION_ID" https://example.com
go run ./cmd/semcrawl content "$SESSION_ID"
go run ./cmd/semcrawl extract "$SESSION_ID" \
  --anchor-text "Learn more" \
  --target "documentation page cards" \
  --field label="the visible link text" \
  --field url="the destination URL" \
  --fields detail="Learn more" \
  --fields detail_url="the destination URL" \
  --table rooms \
  --table-column rooms="Floor" \
  --table-column rooms="Rent/Management fee" \
  --limit 20
go run ./cmd/semcrawl close "$SESSION_ID"
```

`--target` describes records for semantic extraction. `--anchor-text` instead
provides exact visible text to locate first; each matching link is mapped to its
nearest list item or article as a record root. When it is present, `--target` is
optional and acts as context for Jev-selected fields. Each `--field` is written
as `name=description`: the name becomes the JSON key, and the description tells
Jev what value to select. The response includes an `items` array; each item has
a `node_id`, a match `score`, and a `values` object. The response also reports
the Jev model and token usage. `--limit` sets the maximum number of matching
items returned (up to 100), after candidate search and verification. One to twelve
fields can be requested. For name or title fields such as
`--field name="property name"`, `values` also includes `name_url` when the
selected name element has a link, contains a link, or is inside a link. Relative
links are resolved against the current page URL; Semcrawl does not open the
destination.

Use repeatable `--fields name=description` for values that repeat inside one
record, such as room rows within a building. These fields are returned together
as `values.rows`, an array of objects. When the field description exactly
matches `--anchor-text`, Semcrawl uses that exact text node directly rather
than asking Jev to choose it. Without `--anchor-text`, repeated fields may
search up to three ancestor levels by default; `--ancestor-depth` changes that
limit from 1 to 8 and stops before an ancestor whose text repeats the selected
single `--field` name more than once.

Use `--table name` with repeatable `--table-column name=header` to return a
whole table selected by its headers. Each named table is located separately
within every matched record, so one record can return multiple tables. A table
must contain all specified header texts; if multiple tables match the same
name within one record, extraction reports an ambiguity error. Each
`values.<name>` contains `headers` and ordered `rows`; every cell includes its
visible `text`, original cell `html`, and matching `header` where available.
Table extraction can be used without `--field` or `--fields`.

Extraction first groups usable DOM nodes by tag and the complete, sorted class
set. Jev evaluates group summaries with up to three samples, then verifies the
actual nodes in selected groups in batches of 20. If no records match, Jev
locates requested field elements and evaluates their ancestors from the nearest
level upward, sharing checks across field anchors. Matching records retain DOM
order; `--limit` is applied before field extraction.

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
