# Product Requirements Document (PRD): Semcrawl

## 1. Overview & Vision

### 1.1 Problem Statement
Traditional web scraping requires brittle, site-specific implementations heavily dependent on concrete CSS selectors or XPath expressions. When website layouts or class names change, scrapers break immediately and require costly manual updates. Furthermore, managing headless browser lifecycles across standalone scripts often leads to resource leaks or clumsy boilerplate code.

### 1.2 Vision
**Semcrawl** introduces semantic web scraping powered by **Jev** evaluation. Instead of relying on hardcoded CSS selectors or brittle DOM paths, Semcrawl dynamically identifies relevant DOM nodes by their semantic meaning and structural role (e.g., `page.semantic("Search Results > Product List > Product")`).

By abstracting site-specific structures into intuitive semantic queries and providing a session-based Go CLI with an on-demand background daemon, Semcrawl enables resilient, concurrent, and cross-language web scraping.

---

## 2. Target Users & Use Cases

- **Target Audience:** Developers (Backend, Data Engineers, Automation Specialists) across various programming languages (Ruby, Python, Go, Node.js, etc.) who need robust and maintainable scraping pipelines.
- **Core Use Cases:**
  - **Dynamic E-Commerce Scraping:** Extracting product lists, names, prices, and availability across different online storefronts without maintaining custom parsers per site.
  - **Content Aggregation:** Collecting articles, listings, or search results that adapt automatically to UI refactoring.
  - **Language-Agnostic Scraping Scripts:** Writing idiomatic scrapers in any language (e.g., Ruby scripts wrapping CLI commands) while delegating browser lifecycle, session isolation, and DOM classification to Semcrawl.

---

## 3. System Architecture

Semcrawl operates with a **Daemon + CLI** architecture with on-demand daemon lifecycle and session isolation:

```
+-----------------------------------------------------------+
| Multi-language SDK / Scripts (e.g., Ruby, Python, Shell)   |
+-----------------------------+-----------------------------+
                              | Invokes CLI commands with <session_id>
                              v
+-----------------------------------------------------------+
|                   Semcrawl CLI (Go binary)                |
|  - Auto-starts daemon if not currently running            |
+-----------------------------+-----------------------------+
                              | IPC / Unix Socket / HTTP
                              v
+-----------------------------------------------------------+
|                 Semcrawl Daemon (Go process)               |
|                                                           |
|  +-----------------------------------------------------+  |
|  | Session Manager                                     |  |
|  | - Session isolation (tabs/contexts)                 |  |
|  | - Inactivity timeout / auto-cleanup                 |  |
|  +-----------------------------------------------------+  |
|                                                           |
|  +--------------------+             +------------------+  |
|  | Browser Controller |             |  Jev Semantic    |  |
|  | (Chrome CDP /      | <=========> |  Evaluator       |  |
|  |  Firefox BiDi)     |             |  Engine          |  |
|  +--------------------+             +------------------+  |
|           |                                               |
|           v (Future: Lightweight HTTP / curl Fetcher)     |
+-----------------------------------------------------------+
```

### 3.1 Components

1. **Semcrawl CLI:**
   - Lightweight command-line interface communicating with the running daemon.
   - Transparently launches the daemon in the background if it is not already running (via `semcrawl open`).
   - Emits structured JSON outputs to `stdout` for effortless parsing and pipeline chaining.
2. **Semcrawl Daemon & Session Manager:**
   - Long-running background process managing browser sessions (Chrome via CDP, Firefox via WebDriver BiDi).
   - Manages discrete **Sessions** identified by unique `session_id` tokens, isolating browser contexts, tabs, and cookies.
   - Monitors inactivity per session and automatically closes browser resources after a configurable idle duration.
   - Automatically terminates the daemon process when no active sessions remain after an extended idle period.
   - Pluggable fetcher layer: supports full headless browsers, with architecture designed to accommodate lightweight HTTP fetchers (e.g., curl/HTML parser) when full rendering is unnecessary.
3. **Jev Semantic Evaluator Engine:**
   - Evaluates whether candidate DOM elements fulfill the semantic criteria specified in the query.
   - Decoupled and reusable: can integrate with reliable external Jev libraries or lightweight native Go bindings.

---

## 4. Key Functional Requirements

### 4.1 On-Demand Lifecycle & Session Management
- **Auto-Start Daemon:** If the daemon is not running when `semcrawl open` is called, the CLI automatically launches the daemon process.
- **Session Lifecycle:**
  - `open`: Creates an isolated browser session/context and returns a unique `session_id`.
  - `close`: Destroys the specified session and releases associated browser pages/contexts.
  - `session list`: Lists active sessions and their status/idle duration.
- **Idle Timeout & Auto-Cleanup:**
  - Automatically terminates a session if no requests are received for a specified duration (e.g., default: 5–10 minutes).
  - Automatically shuts down the daemon if idle with 0 active sessions for a configurable period (e.g., 15 minutes).
- **Explicit Daemon Controls:** `semcrawl daemon start`, `status`, and `stop` are retained for debugging, monitoring, and explicit service administration.

### 4.2 Page Navigation & Actions
- Navigate to target URLs scoped to a session (`goto <session_id> <url>`).
- Perform basic user interactions (click, scroll, type) scoped to the session while maintaining context and state.

### 4.3 Semantic Query & DOM Traversal
- **Hierarchical Semantic Path Query:**
  - Supports queries in breadcrumb-like syntax: `"Search Results > Product List > Product"`.
  - Top-down traversal within the active session's DOM:
    1. Identify the container matching the first segment (e.g., "Search Results").
    2. Within that container, identify the repeating list container (e.g., "Product List").
    3. Identify repeating items (e.g., "Product") and return an array of matched node identifiers.
- **Sub-Element Attribute & Text Extraction:**
  - Extract specific fields from resolved nodes (e.g., query `"Product Name, Price"` against a `Product` node).
  - Returns extracted structured data per field (text content, attributes like `href`, `src`).

### 4.4 Multi-Language Friendly Output
- All CLI commands support JSON output formatting with clear status codes and error messages.

---

## 5. CLI & SDK Interface Design

### 5.1 CLI Commands

```bash
# Session Lifecycle (Daemon starts automatically if not running)
semcrawl open [--headless] [--browser=chrome|firefox]
# Output: {"session_id": "abc123", "status": "ready"} (or raw session ID in plaintext mode)

# Page Navigation
semcrawl goto abc123 "https://example.com/search?q=laptop"

# Semantic Querying (returns list of node IDs in JSON)
semcrawl query abc123 "検索結果 > 商品一覧 > 商品"

# Data Extraction on Node(s)
semcrawl extract abc123 <node_id> "商品名, 価格"

# Combined or One-liner queries
semcrawl extract abc123 --query "検索結果 > 商品一覧 > 商品" --fields "商品名, 価格"

# Close Session
semcrawl close abc123

# Session & Daemon Management
semcrawl session list
semcrawl daemon status
semcrawl daemon stop
semcrawl daemon start [--headless] [--browser=chrome|firefox]
```

### 5.2 Multi-Language SDK Concept (Ruby Example)

The session-based architecture maps cleanly to language idioms (e.g., block-scoped session handling):

```ruby
Semcrawl.open("https://example.com/search?q=laptop") do |page|
  # Internally executes:
  # session_id = semcrawl open
  # semcrawl goto <session_id> <url>
  products = page.semantic("検索結果 > 商品一覧 > 商品")
  
  products.each do |product|
    data = product.semantic("商品名, 価格")
    puts "#{data['商品名']}: #{data['価格']}"
  end
  # Ensure block exits by calling: semcrawl close <session_id>
end
```

---

## 6. Non-Functional Requirements

- **Performance & Efficiency:**
  - Minimize Jev evaluation overhead by pruning candidate DOM nodes (e.g., ignoring script/style tags, empty containers, or non-visible elements).
  - Low-latency IPC between CLI and Daemon (Unix domain socket or local HTTP/gRPC).
  - Session idle auto-cleanup prevents memory leaks and zombie browser processes.
- **Extensibility:**
  - Fetcher abstraction: easily toggle between full browser (CDP/BiDi) and fast static HTTP fetchers.
  - Modular Jev integration layer to swap or upgrade evaluation strategies.
- **Portability:**
  - Packaged as a single standalone Go binary across Linux, macOS, and Windows.

---

## 7. Future Enhancements (Roadmap)

1. **Selector Caching & Self-Healing:**
   - Once Jev identifies the concrete DOM selector path, cache the selector for subsequent queries to accelerate repeated scraping runs.
   - Automatically fall back to Jev re-evaluation if the cached selector fails (self-healing scrapers).
2. **Lightweight Headless HTTP Fetcher:**
   - Add curl/HTTP client mode for static pages where headless browser rendering is redundant.
3. **Official Client Bindings:**
   - Provide minimal, idiomatic gems/packages (Ruby, Python, Node.js) wrapping the CLI/Daemon protocol with automatic block-scoped session cleanup.
