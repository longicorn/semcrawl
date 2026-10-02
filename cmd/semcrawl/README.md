# semcrawl CLI

`open` starts the daemon automatically when needed and creates a headless
Chrome session. Commands return JSON on stdout:

```sh
export TYPESAFE_API_KEY=...
go run ./cmd/semcrawl open
# {"session_id":"...","status":"active",...}
go run ./cmd/semcrawl goto <session_id> https://example.com
go run ./cmd/semcrawl content <session_id>
go run ./cmd/semcrawl extract <session_id> \
  --target "product cards" \
  --anchor-text "View details" \
  --field name="product name" \
  --field price="price" \
  --fields label="View details" \
  --fields detail_url="destination URL" \
  --ancestor-depth 3
go run ./cmd/semcrawl close <session_id>
```

`content` returns the final URL, title, and rendered HTML. `session list`
shows active sessions. `daemon start`, `daemon status`, and `daemon stop`
control the daemon explicitly. Sessions expire after 10 minutes without a
command; an idle daemon exits after 15 minutes with no sessions.

`extract` sends compact summaries of visible DOM elements to the TypeSafe Jev
API. Set `TYPESAFE_API_KEY` before starting the daemon (or before the first
`open`, which starts it automatically). The `--target` and each `--field`
description are natural language; field names become keys in the JSON output.
Set `SEMCRAWL_JEV_CONCURRENCY` before daemon startup to control concurrent Jev
evaluations (default `2`, range `1`–`8`); restart the daemon after changing it.
Use `--limit` to control the maximum number of matching result items returned
(default 20, maximum 100). Tag/class groups are shortlisted before actual nodes are verified in batches.
When no records match, requested field elements seed a bottom-up ancestor
search. The return limit is applied after matching, before field extraction.
The response contains an `items` array with one `values` object and a Jev match
score per item, along with the model and reported token usage.
For name or title fields, the values object also includes `<field>_url` when a
link is attached to the selected element, nested inside it, or wraps it. Relative
URLs are resolved against the page URL, and the destination is not opened.

`--anchor-text` locates exact visible text first and uses its nearest list item
or article as the record root. In this mode `--target` is optional and provides
context for Jev-selected fields. A `--fields` description equal to the anchor
text uses the exact matched node directly; URL fields use the matched anchor's
`href`. Repeatable `--fields` selects repeated values and returns them together
as `values.rows`, an array of objects. Without `--anchor-text`, the default
ancestor search depth is 3; `--ancestor-depth` accepts values from 1 to 8 and
search stops before an ancestor that contains the selected single name more
than once.

The daemon uses a Unix domain socket at `$XDG_RUNTIME_DIR/semcrawl.sock`, or
`<user config directory>/semcrawl/semcrawl.sock` when that environment variable
is unset. The socket is restricted to the current user. Chrome or Chromium
must be installed.

The single-command convenience operation is also available:

```sh
go run ./cmd/semcrawl scrape https://example.com
```
