# Architecture Decision Records (ADRs): Semcrawl

This document outlines key architectural decisions made for the Semcrawl project.

---

## Table of Contents
- [ADR-001: Daemon + CLI Architecture with On-Demand Lifecycle](#adr-001-daemon--cli-architecture-with-on-demand-lifecycle)
- [ADR-002: Inter-Process Communication via Unix Domain Socket](#adr-002-inter-process-communication-via-unix-domain-socket)
- [ADR-003: Headless Browser Driver using `chromedp` with Fetcher Abstraction](#adr-003-headless-browser-driver-using-chromedp-with-fetcher-abstraction)
- [ADR-004: In-House Jev Evaluator with `TYPESAFE_API_KEY` and DOM Pruning](#adr-004-in-house-jev-evaluator-with-typesafe_api_key-and-dom-pruning)

---

## ADR-001: Daemon + CLI Architecture with On-Demand Lifecycle

### Status
Accepted

### Context
Headless browser processes (e.g., Chromium) are resource-heavy and slow to boot. In traditional scraping scripts, launching a browser per CLI command or standalone script introduces major startup latency, risks zombie processes, and prevents reusing authenticated sessions or navigation contexts across sequential commands.

Conversely, requiring users to manually manage background daemons (e.g., `systemctl`, background service managers) adds operational friction, especially for one-off CLI scripting and lightweight SDK integrations.

### Decision
We adopt a **Daemon + CLI architecture with on-demand lifecycle**:
1. **On-Demand Auto-Start:** The Semcrawl CLI automatically launches the daemon in the background if it is not already running upon executing commands like `semcrawl open`.
2. **Session Isolation:** The daemon manages individual scraping sessions (`session_id`), isolating browser contexts, cookies, and page tabs.
3. **Inactivity Auto-Cleanup:**
   - Sessions are terminated if no commands are received within a configurable idle duration (default: 5–10 minutes).
   - The daemon process terminates itself automatically when there are zero active sessions for an extended idle period (e.g., 15 minutes).
4. **Explicit Management:** Manual controls (`semcrawl daemon start`, `status`, `stop`) are retained for debugging, monitoring, and controlled service execution.

### Consequences
- **Positive:**
  - High responsiveness for subsequent CLI calls by avoiding repeated browser startup overhead.
  - No orphaned processes or persistent memory leaks due to automated timeouts.
  - Seamless user experience: users do not need to manually boot or stop a background service.
- **Negative:**
  - Added architectural complexity in tracking session states, file locks, and background process spawning.

---

## ADR-002: Inter-Process Communication via Unix Domain Socket

### Status
Accepted

### Context
The Semcrawl CLI and the background Daemon run on the same local host and require low-latency, bidirectional or request-response communication to coordinate navigation, DOM querying, and data extraction.

Options considered:
- **Local TCP HTTP / gRPC:** Simple, but introduces TCP port binding conflicts, firewall prompts, and security risks if bound to non-loopback interfaces.
- **Standard Input/Output (stdio):** Tight coupling to a single process hierarchy; difficult to share across multiple CLI calls and concurrent sessions.
- **Unix Domain Socket (UDS) / Named Pipes:** File-system-based IPC, avoiding port conflicts and offering fast, secure inter-process communication with OS-level permission control.

### Decision
We use **Unix Domain Sockets (UDS)** (and Windows Named Pipes on Windows) running an HTTP/REST-like JSON protocol:
1. The daemon creates and listens on a dedicated socket file (e.g., `$XDG_RUNTIME_DIR/semcrawl.sock` or `~/.semcrawl/semcrawl.sock`).
2. The CLI connects to this socket file directly.
3. Communications use lightweight HTTP/1.1 with JSON payloads over UDS using Go's standard library (`net.Listen("unix", ...)` and `http.Client` with custom `DialContext`).

### Consequences
- **Positive:**
  - Fast, zero-port-conflict communication protected by file system permissions.
  - Clean separation between daemon lifecycle and individual CLI invocations.
  - Standard JSON schemas allow easy debugging and future client SDK integrations.
- **Negative:**
  - Requires platform-specific handling for Windows (using Named Pipes or fallback localhost port).

---

## ADR-003: Headless Browser Driver using `chromedp` with Fetcher Abstraction

### Status
Accepted

### Context
To perform semantic scraping on dynamic Single Page Applications (SPAs) and modern web pages, a dependable browser automation driver is essential. In the Go ecosystem, common options include `chromedp`, `rod`, or external tools like Playwright/Puppeteer via Node.js bridges.

Stability, maturity, zero external runtime dependencies (pure Go), and alignment with future extensibility (WebDriver BiDi, lightweight static HTTP fetchers) were prioritized.

### Decision
1. **Adopt `chromedp` as Primary Driver:**
   - Use `chromedp` due to its maturity, extensive production track record, and native Chrome DevTools Protocol (CDP) support in pure Go without Node.js dependencies.
2. **Define a Decoupled `Fetcher` Interface:**
   - Define a generic `Fetcher` interface in the daemon layer that abstracts page navigation, element evaluation, and DOM extraction.
   - Initial implementation: `ChromeDPFetcher`.
   - Planned implementations: `BiDiFetcher` (for Firefox/cross-browser support) and `HTTPFetcher` (for lightweight static page requests without browser rendering).

### Consequences
- **Positive:**
  - Stable, well-tested browser control with minimal Go external dependencies.
  - Clear architectural boundary allowing seamless introduction of non-browser or multi-browser backends in the future.
- **Negative:**
  - Direct CDP commands can be verbose and require careful context and timeout management.

---

## ADR-004: In-House Jev Evaluator with `TYPESAFE_API_KEY` and DOM Pruning

### Status
Accepted

### Context
Semcrawl's core differentiator is semantic element identification via **Jev** evaluation instead of brittle CSS/XPath selectors. The evaluator must:
- Accurately parse hierarchical semantic queries (e.g., `"Search Results > Product List > Product"`).
- Interface with external AI/LLM evaluation APIs or semantic models reliably.
- Minimize performance overhead and latency caused by serializing and processing large DOM trees.

### Decision
1. **In-House Go Implementation:**
   - Implement the Jev query parser and evaluator natively in Go to ensure tight integration, predictable memory consumption, and clean error handling.
2. **API Key Configuration:**
   - The evaluator authenticates against necessary evaluation APIs using the environment variable `TYPESAFE_API_KEY`.
3. **DOM Pruning Strategy:**
   - Avoid transferring full, unpruned DOM snapshots to the evaluation engine.
   - Filter out invisible elements (`display: none`, `visibility: hidden`), structural noise (`<script>`, `<style>`, `<iframe>`, comments), and redundant wrappers before semantic classification.
   - Traverse hierarchically segment-by-segment to narrow the search space before evaluating descendants.

### Consequences
- **Positive:**
  - Full control over parsing logic, caching mechanisms, and external API invocation.
  - Significantly reduced token count, bandwidth, and latency through pre-evaluation DOM pruning.
  - Standardized configuration via `TYPESAFE_API_KEY`.
- **Negative:**
  - Maintenance overhead of maintaining custom AST/parser logic and pruning heuristics within the Semcrawl codebase.

---

## ADR-005: Batched Tag/Class Search with Field-Anchored Fallback

### Status
Accepted

### Context
The previous representative search evaluated one DOM node per Jev request until
its first match. Although repeated siblings were prioritized, unrelated nodes
before a matching record caused many sequential network round trips. Expansion
by a reusable class could also include unrelated elements without verification.

### Decision
1. Reuse the browser DOM snapshot and the existing Jev evaluator and field
   extraction interfaces. Include usable non-landmark containers in search.
2. Before grouping, skip unclassified `div` and `span` wrappers whose rendered
   text exactly repeats their parent's text. Their text remains on the parent;
   wrappers with direct text, classes, roles, or useful attributes remain
   candidates. Group the remaining nodes by tag and the complete sorted class
   token set, independent of class order or parent. Preserve complete class
   strings in the snapshot.
   The browser snapshot also omits empty unclassified `div`/`span` leaves and
   `br`/`hr`/`wbr` separators; rendered text is collected from their surrounding
   elements. This follows the wrapper-collapse idea used by
   [mcprune](https://github.com/hamr0/mcprune), with a narrower rule for the
   rendered DOM so repeated cards, tables, links, and field-bearing nodes stay intact.
3. Evaluate group summaries in batches of 20 groups. Include counts and at most
   three samples spread across each group, with text and attributes, so opaque
   class names do not need to carry semantic meaning.
4. Verify every node in shortlisted groups against the target, in batches of 20
   nodes. Run independent group and node batches with bounded concurrent Jev
   evaluations, configured by `SEMCRAWL_JEV_CONCURRENCY` (default 2, range 1–8).
   Sharing tag/class alone does not establish a record match.
5. If no records match, evaluate field-bearing nodes in batches against the
   requested field descriptions and target. Retain all matching anchors, then
   evaluate deduplicated candidates starting at the anchors and proceeding to
   their nearest ancestors. Stop each matched branch. Prefer matched descendants
   over matching ancestors reached through other branches.
6. Preserve DOM output order, aggregate model usage across all stages, and apply
   the result limit before reusing representative-based field extraction. API
   failures and malformed answers remain errors, rather than triggering fallback.

### Consequences
- Fewer sequential requests when many nodes share a small number of signatures.
- Independent search batches overlap at the configured concurrency; Jev 429
  and 529 responses are retried with exponential backoff.
- Group summaries bound sample payload; verification prevents unconditional
  class-based expansion. Groups with unique utility class sets can remain costly.
- Group samples can miss heterogeneous content. Bottom-up fallback recovers
  records only when a relevant field anchor is recognized; it is not exhaustive.
- The fallback can be expensive on large pages, but no longer sends one request
  per field node or ancestor. CLI arguments and JSON result structure are unchanged.
- Mocked tests compare request counts and serialized payload sizes, verify
  unrelated nodes are rejected, and cover shared ancestors at differing depths.
  Real API latency and classification accuracy require live-page measurement.

## ADR-006: Repeated Fields Within a Matched Record

### Status
Accepted

### Context
Some result records contain a scalar parent section and a repeated sibling
region, such as one building with multiple room rows. Representative field
selection from only the matched node can omit those sibling rows or collapse
them into one value.

### Decision
1. Keep `--field name=description` for one value per matched record and add
   repeatable `--fields name=description` for values that belong to repeated
   sibling regions.
2. Return repeated fields together as `values.rows`, an ordered array of
   objects, while preserving the outer matched record and its scalar values.
3. Permit a bounded ancestor search to include sibling branches. The default
   depth is 3 and `--ancestor-depth` accepts values from 1 to 8. Stop before an
   ancestor whose text contains the selected scalar name more than once.

### Consequences
- Building fields remain scalar while room-row fields stay grouped together.
- The repeated region is inferred from the selected fields' common DOM
  ancestor and repeated tag/class signature; unusual markup may need a depth
  adjustment or more descriptive fields.
- A repeated item without a selected scalar name uses the configured depth
  limit as its boundary.

## ADR-007: Exact Text Anchors for Record Discovery

### Status
Accepted

### Context
Broad semantic targets can match both complete records and their nested
children. Pages often expose a stable, explicit text such as a detail-link
label that identifies the relevant record region more reliably than a
semantic guess.

### Decision
1. Add optional `--anchor-text` for exact visible-text matching. When supplied,
   use each match's nearest list-item or article ancestor as the record root
   and deduplicate roots before field extraction.
2. Use fields whose descriptions match the anchor text, and requested link URL
   fields, directly from the matched anchor node. Use Jev for less explicit
   fields within the chosen record root.
3. Keep `--target` as optional semantic context when `--anchor-text` is present;
   without an anchor, retain the existing semantic record search.

### Consequences
- Exact text anchors bypass broad semantic candidate matching and reduce
  accidental child-node records.
- Pages without list-item or article wrappers fall back to the exact matching
  node as the record root; callers may need semantic search for other layouts.
