package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestChromeDOMSnapshot(t *testing.T) {
	if os.Getenv("SEMCRAWL_BROWSER_TEST") != "1" {
		t.Skip("set SEMCRAWL_BROWSER_TEST=1 to run the Chrome integration test")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Product fixture</title></head><body><main><ul><li class="product"><h2>Example Product</h2><span>$12</span></li><li class="product"><h2>Second Product</h2><span>$20</span></li></ul></main></body></html>`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	factory := NewChromeFactory(ctx)
	defer factory.Close()
	tab, err := factory.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tab.Close()
	if _, err := tab.Navigate(ctx, server.URL); err != nil {
		t.Fatal(err)
	}
	snapshot, err := tab.DOMSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Title == "" || !strings.Contains(snapshot.URL, server.URL) {
		t.Fatalf("snapshot metadata = %+v", snapshot)
	}
	var found bool
	for _, node := range snapshot.Nodes {
		if node.Tag == "li" && strings.Contains(node.Class, "product") && strings.Contains(node.Text, "Example Product") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("product card missing from DOM snapshot: %+v", snapshot.Nodes)
	}
}
