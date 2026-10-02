package semantic

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/longicorn/semcrawl/internal/browser"
	"github.com/longicorn/semcrawl/jev"
)

func TestGroupedSearchBatchesAndRejectsUnrelatedNodes(t *testing.T) {
	page := browser.DOMSnapshot{}
	for i := 0; i < 100; i++ {
		page.Nodes = append(page.Nodes, browser.DOMNode{ID: fmt.Sprintf("noise%d", i), Tag: "a", Class: "navigation", Text: "Unrelated navigation link", Order: i})
	}
	page.Nodes = append(page.Nodes,
		browser.DOMNode{ID: "record", Tag: "article", Class: "card product", Text: "A matching product", Order: 100},
		browser.DOMNode{ID: "unrelated", Tag: "article", Class: "product card", Text: "An unrelated promotion", Order: 101})
	calls, bytes, verified := 0, 0, 0
	evaluator := fakeEvaluator(func(_ context.Context, request jev.Request) (*jev.Response, error) {
		calls++
		payload, _ := json.Marshal(request)
		bytes += len(payload)
		answers := map[string]jev.Answer{}
		if groups, ok := request.State.(map[string]any)["groups"].([]map[string]any); ok {
			if len(groups) != 2 {
				t.Fatalf("got %d groups, want 2", len(groups))
			}
			if groups[0]["count"] != 100 || groups[1]["count"] != 2 {
				t.Fatalf("unexpected group counts: %v", groups)
			}
			if len(groups[0]["samples"].([]map[string]any)) != 3 {
				t.Fatal("group summaries must bound sample count")
			}
		}
		for name := range request.Questions {
			score := 0.0
			if name == "group_g1" || name == "match_record" {
				score = 0.99
			}
			if strings.HasPrefix(name, "match_") {
				verified++
			}
			answers[name] = jev.Answer{Type: jev.QuestionNoul, Noul: &score}
		}
		return &jev.Response{Answers: answers, Usage: jev.Usage{InputTokens: 7, OutputTokens: 2}}, nil
	})
	matches, usage, _, err := NewExtractor(evaluator).findGroupedMatches(context.Background(), page, "products", []Field{{Name: "name", Description: "product name"}}, groupedCandidates(page.Nodes))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].node.ID != "record" || calls != 2 || verified != 2 {
		t.Fatalf("matches=%v calls=%d verified=%d", matches, calls, verified)
	}
	if usage.InputTokens != 14 || usage.OutputTokens != 4 {
		t.Fatalf("usage=%+v", usage)
	}
	oldCalls, oldBytes := 0, 0
	baseline := NewExtractor(fakeEvaluator(func(_ context.Context, request jev.Request) (*jev.Response, error) {
		oldCalls++
		payload, _ := json.Marshal(request)
		oldBytes += len(payload)
		answers := map[string]jev.Answer{}
		for name := range request.Questions {
			score := 0.0
			if name == "match_record" {
				score = 0.99
			}
			answers[name] = jev.Answer{Type: jev.QuestionNoul, Noul: &score}
		}
		return &jev.Response{Answers: answers}, nil
	}))
	for _, item := range makeCandidates(page.Nodes, len(page.Nodes)) {
		found, _, _, err := baseline.findMatches(context.Background(), page, "products", []candidate{item})
		if err != nil {
			t.Fatal(err)
		}
		if len(found) > 0 {
			break
		}
	}
	if oldCalls != 101 || bytes >= oldBytes {
		t.Fatalf("baseline calls=%d bytes=%d; grouped bytes=%d", oldCalls, oldBytes, bytes)
	}
	t.Logf("102 nodes / 2 groups: grouped verification %d requests / %d request bytes; previous search to first match %d requests / %d bytes (fake evaluator, field extraction excluded)", calls, bytes, oldCalls, oldBytes)
}

func TestFieldFallbackFindsAllAnchorsAndDeduplicatesAncestors(t *testing.T) {
	page := browser.DOMSnapshot{Nodes: []browser.DOMNode{
		{ID: "root", Tag: "body", Text: "Whole product list", Order: 0},
		{ID: "a", ParentID: "root", Tag: "div", Text: "Alpha product", Order: 1},
		{ID: "a-name", ParentID: "a", Tag: "span", DirectText: "Alpha", Text: "Alpha", Order: 2},
		{ID: "a-price", ParentID: "a", Tag: "span", DirectText: "$10", Text: "$10", Order: 3},
		{ID: "b", ParentID: "root", Tag: "div", Text: "Beta product", Order: 4},
		{ID: "wrapper", ParentID: "b", Tag: "div", Order: 5},
		{ID: "b-name", ParentID: "wrapper", Tag: "span", DirectText: "Beta", Text: "Beta", Order: 6},
	}}
	checked := map[string]int{}
	calls := 0
	evaluator := fakeEvaluator(func(_ context.Context, request jev.Request) (*jev.Response, error) {
		calls++
		answers := map[string]jev.Answer{}
		for name := range request.Questions {
			score := 0.0
			if strings.HasPrefix(name, "anchor_") {
				score = 0.99
			}
			if strings.HasPrefix(name, "match_") {
				checked[strings.TrimPrefix(name, "match_")]++
			}
			if name == "match_a" || name == "match_b" {
				score = 0.99
			}
			answers[name] = jev.Answer{Type: jev.QuestionNoul, Noul: &score}
		}
		return &jev.Response{Answers: answers, Model: "fallback", Usage: jev.Usage{InputTokens: 1}}, nil
	})
	matches, usage, model, err := NewExtractor(evaluator).findGroupedMatches(context.Background(), page, "products", []Field{{Name: "name", Description: "product name"}}, groupedCandidates(page.Nodes))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].node.ID != "a" || matches[1].node.ID != "b" {
		t.Fatalf("matches=%+v", matches)
	}
	if checked["a"] != 1 || checked["root"] != 0 {
		t.Fatalf("ancestor checks=%v", checked)
	}
	if calls != 5 || usage.InputTokens != calls || model != "fallback" {
		t.Fatalf("calls=%d usage=%+v model=%s", calls, usage, model)
	}
}

func TestGroupedSearchPropagatesMalformedAnswers(t *testing.T) {
	page := browser.DOMSnapshot{Nodes: []browser.DOMNode{{ID: "a", Tag: "article", Text: "A product"}}}
	evaluator := fakeEvaluator(func(context.Context, jev.Request) (*jev.Response, error) {
		return &jev.Response{Answers: map[string]jev.Answer{}}, nil
	})
	_, err := NewExtractor(evaluator).Extract(context.Background(), page, "products", []Field{{Name: "name", Description: "name"}}, 10)
	if err == nil || !strings.Contains(err.Error(), "group_g0") {
		t.Fatalf("error=%v", err)
	}
}

func TestNormalizedClassKeepsAllTokens(t *testing.T) {
	if normalizedClass("e d c b a") != normalizedClass("a b c d e") {
		t.Fatal("class order should not affect grouping")
	}
	if normalizedClass("a b c d e") == normalizedClass("a b c d f") {
		t.Fatal("classes after the fourth token must distinguish groups")
	}
}
