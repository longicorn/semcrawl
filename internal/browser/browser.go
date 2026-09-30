package browser

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/chromedp"
)

// Page is the rendered state of a browser tab.
type Page struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	HTML  string `json:"html"`
}

// DOMNode is a compact description of one visible page element.
type DOMNode struct {
	ID         string            `json:"id"`
	ParentID   string            `json:"parent_id,omitempty"`
	Order      int               `json:"order"`
	Tag        string            `json:"tag"`
	Role       string            `json:"role,omitempty"`
	Class      string            `json:"class,omitempty"`
	AriaLabel  string            `json:"aria_label,omitempty"`
	Text       string            `json:"text,omitempty"`
	DirectText string            `json:"direct_text,omitempty"`
	Href       string            `json:"href,omitempty"`
	Src        string            `json:"src,omitempty"`
	Alt        string            `json:"alt,omitempty"`
	Title      string            `json:"title,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// DOMSnapshot is a page snapshot with visible DOM elements for semantic work.
type DOMSnapshot struct {
	URL   string    `json:"url"`
	Title string    `json:"title"`
	Nodes []DOMNode `json:"nodes"`
}

// Tab is one isolated browser target.
type Tab interface {
	Navigate(context.Context, string) (Page, error)
	Snapshot(context.Context) (Page, error)
	DOMSnapshot(context.Context) (DOMSnapshot, error)
	Close() error
}

// Factory creates tabs and owns the shared browser process.
type Factory interface {
	Open(context.Context) (Tab, error)
	Close() error
}

// ChromeFactory manages one Chrome process shared by isolated tabs.
type ChromeFactory struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func NewChromeFactory(parent context.Context) *ChromeFactory {
	ctx, cancel := chromedp.NewExecAllocator(parent, chromedp.DefaultExecAllocatorOptions[:]...)
	return &ChromeFactory{ctx: ctx, cancel: cancel}
}

func (f *ChromeFactory) Open(ctx context.Context) (Tab, error) {
	tabCtx, cancel := chromedp.NewContext(f.ctx)
	if err := chromedp.Run(tabCtx); err != nil {
		cancel()
		return nil, fmt.Errorf("start Chrome tab: %w (install Chrome or Chromium if no browser is available)", err)
	}
	return &chromeTab{ctx: tabCtx, cancel: cancel}, nil
}

func (f *ChromeFactory) Close() error {
	f.cancel()
	return nil
}

type chromeTab struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (t *chromeTab) Navigate(ctx context.Context, rawURL string) (Page, error) {
	opCtx, cancel := context.WithTimeout(t.ctx, time.Minute)
	defer cancel()
	stopCancel := context.AfterFunc(ctx, cancel)
	defer stopCancel()
	var page Page
	err := chromedp.Run(opCtx,
		chromedp.Navigate(rawURL),
		chromedp.Evaluate(`({url: location.href, title: document.title, html: document.documentElement ? document.documentElement.outerHTML : ""})`, &page),
	)
	if err != nil {
		return Page{}, err
	}
	return page, nil
}

func (t *chromeTab) Snapshot(ctx context.Context) (Page, error) {
	opCtx, cancel := context.WithTimeout(t.ctx, 15*time.Second)
	defer cancel()
	stopCancel := context.AfterFunc(ctx, cancel)
	defer stopCancel()
	var page Page
	if err := chromedp.Run(opCtx, chromedp.Evaluate(`({url: location.href, title: document.title, html: document.documentElement ? document.documentElement.outerHTML : ""})`, &page)); err != nil {
		return Page{}, err
	}
	return page, nil
}

func (t *chromeTab) DOMSnapshot(ctx context.Context) (DOMSnapshot, error) {
	opCtx, cancel := context.WithTimeout(t.ctx, 20*time.Second)
	defer cancel()
	stopCancel := context.AfterFunc(ctx, cancel)
	defer stopCancel()
	const script = `(() => {
  const clean = value => (value || "").replace(/\s+/g, " ").trim();
  const visible = el => {
    const style = getComputedStyle(el);
    const rect = el.getBoundingClientRect();
    return style.display !== "none" && style.visibility !== "hidden" && style.opacity !== "0" && rect.width > 0 && rect.height > 0;
  };
  const elements = [document.body, ...document.body.querySelectorAll("*")].filter(el => el && visible(el));
  const ids = new WeakMap();
  elements.forEach((el, index) => ids.set(el, "n" + index));
  const nodes = elements.map((el, order) => {
    const direct = [...el.childNodes].filter(n => n.nodeType === Node.TEXT_NODE).map(n => n.nodeValue).join(" ");
    return {
      id: ids.get(el), parent_id: ids.get(el.parentElement) || "", order,
      tag: el.tagName.toLowerCase(), role: el.getAttribute("role") || "",
      class: typeof el.className === "string" ? el.className.slice(0, 160) : "",
      aria_label: el.getAttribute("aria-label") || "",
      text: clean(el.innerText || el.textContent).slice(0, 700),
      direct_text: clean(direct).slice(0, 300),
      href: typeof el.href === "string" ? (el.href || "") : (el.getAttribute("href") || ""),
      src: el.currentSrc || (typeof el.src === "string" ? (el.src || "") : (el.getAttribute("src") || "")),
      alt: el.getAttribute("alt") || "", title: el.getAttribute("title") || "",
      attributes: (() => {
        const out = {};
        const allowed = /^(data-(price|sku|id|product|item|rating|availability|stock|currency|title|name|date|url)|itemprop|datetime|content|property|rel)$/i;
        for (const attr of el.attributes) {
          if (allowed.test(attr.name) && Object.keys(out).length < 12) out[attr.name] = attr.value.slice(0, 180);
        }
        return out;
      })()
    };
  });
  return {url: location.href, title: document.title, nodes};
})()`
	var snapshot DOMSnapshot
	if err := chromedp.Run(opCtx, chromedp.Evaluate(script, &snapshot)); err != nil {
		return DOMSnapshot{}, err
	}
	return snapshot, nil
}

func (t *chromeTab) Close() error {
	defer t.cancel()
	return chromedp.Cancel(t.ctx)
}
