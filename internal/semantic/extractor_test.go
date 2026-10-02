package semantic

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/longicorn/semcrawl/internal/browser"
	"github.com/longicorn/semcrawl/jev"
)

type fakeEvaluator func(context.Context, jev.Request) (*jev.Response, error)

func (f fakeEvaluator) Evaluate(ctx context.Context, request jev.Request) (*jev.Response, error) {
	return f(ctx, request)
}

func TestExtractFindsRecordsAndSelectsFields(t *testing.T) {
	page := browser.DOMSnapshot{
		URL: "https://shop.example/search", Title: "Search",
		Nodes: []browser.DOMNode{
			{ID: "n0", Tag: "body", Order: 0, Text: "Alpha $10 Beta $20"},
			{ID: "n1", ParentID: "n0", Order: 1, Tag: "div", Class: "products"},
			{ID: "n2", ParentID: "n1", Order: 2, Tag: "article", Class: "product", Text: "Alpha $10 View"},
			{ID: "n3", ParentID: "n2", Order: 3, Tag: "h2", Text: "Alpha", DirectText: "Alpha"},
			{ID: "n4", ParentID: "n2", Order: 4, Tag: "span", Class: "price", Text: "$10", DirectText: "$10"},
			{ID: "n5", ParentID: "n2", Order: 5, Tag: "a", Text: "View", DirectText: "View", Href: "/alpha"},
			{ID: "n6", ParentID: "n1", Order: 6, Tag: "article", Class: "product", Text: "Beta $20 View"},
			{ID: "n7", ParentID: "n6", Order: 7, Tag: "h2", Text: "Beta", DirectText: "Beta"},
			{ID: "n8", ParentID: "n6", Order: 8, Tag: "span", Class: "price", Text: "$20", DirectText: "$20"},
			{ID: "n9", ParentID: "n6", Order: 9, Tag: "a", Text: "View", DirectText: "View", Href: "/beta"},
		},
	}
	evaluator := fakeEvaluator(func(_ context.Context, request jev.Request) (*jev.Response, error) {
		answers := make(map[string]jev.Answer)
		for name, question := range request.Questions {
			if question.Type == jev.QuestionNoul {
				score := 0.0
				if name == "match_n2" || name == "match_n6" || name == "group_g1" {
					score = 0.98
				}
				answers[name] = jev.Answer{Type: jev.QuestionNoul, Noul: &score}
				continue
			}
			var choice string
			switch name {
			case "field_n2_0":
				choice = "n3"
			case "field_n2_1":
				choice = "n4"
			case "field_n2_2":
				choice = "n5"
			case "field_n6_0":
				choice = "n7"
			case "field_n6_1":
				choice = "n8"
			case "field_n6_2":
				choice = "n9"
			default:
				return nil, fmt.Errorf("unexpected question %q", name)
			}
			answers[name] = jev.Answer{Type: jev.QuestionChoice, Choice: choice}
		}
		return &jev.Response{Model: "test-model", Answers: answers, Usage: jev.Usage{InputTokens: 3, OutputTokens: 2}}, nil
	})

	result, err := NewExtractor(evaluator).Extract(context.Background(), page, "product cards", []Field{
		{Name: "name", Description: "product name"},
		{Name: "price", Description: "price"},
		{Name: "url", Description: "product link"},
	}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("got %d items, want 2: %+v", len(result.Items), result.Items)
	}
	if result.Items[0].Values["name"] != "Alpha" || result.Items[0].Values["price"] != "$10" || result.Items[0].Values["url"] != "https://shop.example/alpha" {
		t.Fatalf("unexpected first item: %+v", result.Items[0])
	}
	if result.Items[0].Score != 0.98 {
		t.Fatalf("match score = %v, want 0.98", result.Items[0].Score)
	}
	if result.Items[0].Score != 0.98 {
		t.Fatalf("match score = %v, want 0.98", result.Items[0].Score)
	}
	if result.Items[1].Values["name"] != "Beta" || result.Items[1].Values["price"] != "$20" || result.Items[1].Values["url"] != "https://shop.example/beta" {
		t.Fatalf("unexpected second item: %+v", result.Items[1])
	}
	if result.Usage.InputTokens != 9 || result.Usage.OutputTokens != 6 || result.Model != "test-model" {
		t.Fatalf("usage/model not combined: %+v", result)
	}
}

func TestExtractValidatesFieldsAndCandidateLimit(t *testing.T) {
	page := browser.DOMSnapshot{Nodes: []browser.DOMNode{{ID: "x", Tag: "main", Text: "content"}}}
	extractor := NewExtractor(fakeEvaluator(func(context.Context, jev.Request) (*jev.Response, error) {
		t.Fatal("evaluator should not be called for invalid input")
		return nil, nil
	}))
	if _, err := extractor.Extract(context.Background(), page, "content", nil, 20); err == nil {
		t.Fatal("expected missing-fields error")
	}
	if _, err := extractor.Extract(context.Background(), page, "content", []Field{{Name: "text", Description: "main text"}}, 101); err == nil {
		t.Fatal("expected limit error")
	}
}

func TestExtractReturnsEmptyItemsWhenNoCandidatesMatch(t *testing.T) {
	page := browser.DOMSnapshot{URL: "https://example.com", Nodes: []browser.DOMNode{
		{ID: "n0", Tag: "main", Text: "Unrelated content"},
	}}
	evaluator := fakeEvaluator(func(_ context.Context, request jev.Request) (*jev.Response, error) {
		answers := make(map[string]jev.Answer)
		for name, question := range request.Questions {
			if question.Type == jev.QuestionChoice {
				answers[name] = jev.Answer{Type: jev.QuestionChoice, Choice: "none"}
				continue
			}
			score := 0.1
			answers[name] = jev.Answer{Type: jev.QuestionNoul, Noul: &score}
		}
		return &jev.Response{Answers: answers}, nil
	})
	result, err := NewExtractor(evaluator).Extract(context.Background(), page, "products", []Field{{Name: "name", Description: "product name"}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Items == nil || len(result.Items) != 0 {
		t.Fatalf("items = %#v, want empty array", result.Items)
	}
}

func TestExtractVerifiesNodesInSelectedTagClassGroup(t *testing.T) {
	page := browser.DOMSnapshot{URL: "https://example.com", Nodes: []browser.DOMNode{
		{ID: "root", Tag: "body", Text: "Alpha listing Beta listing"},
		{ID: "a", ParentID: "root", Order: 1, Tag: "div", Class: "result-card active", Text: "Alpha listing"},
		{ID: "a-name", ParentID: "a", Order: 2, Tag: "span", Text: "Alpha", DirectText: "Alpha"},
		{ID: "b", ParentID: "root", Order: 3, Tag: "div", Class: "result-card active", Text: "Beta listing"},
		{ID: "b-name", ParentID: "b", Order: 4, Tag: "span", Text: "Beta", DirectText: "Beta"},
	}}
	evaluations := 0
	evaluator := fakeEvaluator(func(_ context.Context, request jev.Request) (*jev.Response, error) {
		evaluations++
		answers := make(map[string]jev.Answer)
		for name, question := range request.Questions {
			if question.Type == jev.QuestionNoul {
				score := 0.99
				if strings.HasPrefix(name, "group_") {
					score = 0.99
				} else if name != "match_a" && name != "match_b" {
					score = 0
				}
				answers[name] = jev.Answer{Type: jev.QuestionNoul, Noul: &score}
				continue
			}
			choice := "a-name"
			answers[name] = jev.Answer{Type: jev.QuestionChoice, Choice: choice}
		}
		return &jev.Response{Model: "test", Answers: answers}, nil
	})
	result, err := NewExtractor(evaluator).Extract(context.Background(), page, "listing cards", []Field{{Name: "name", Description: "listing name"}}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || evaluations != 3 {
		t.Fatalf("items=%d evaluator calls=%d, want 2 items and one group, node verification, and field call", len(result.Items), evaluations)
	}
	if result.Items[0].NodeID != "a" || result.Items[1].NodeID != "b" {
		t.Fatalf("unexpected expanded items: %+v", result.Items)
	}
	if result.Items[0].Values["name"] != "Alpha" || result.Items[1].Values["name"] != "Beta" {
		t.Fatalf("fields were not copied using the representative selector: %+v", result.Items)
	}
}

func TestFieldValueDistinguishesLinkURLFromLinkLabel(t *testing.T) {
	anchor := browser.DOMNode{Tag: "a", DirectText: "Learn more", Text: "Learn more", Href: "https://iana.org/help/example-domains"}
	if got := fieldValue("destination URL of the link", anchor, "https://example.com/"); got != anchor.Href {
		t.Fatalf("URL field = %v, want %q", got, anchor.Href)
	}
	if got := fieldValue("visible text of the link", anchor, "https://example.com/"); got != "Learn more" {
		t.Fatalf("link label = %v, want visible text", got)
	}
	if got := fieldValue("リンクの表示テキスト", anchor, "https://example.com/"); got != "Learn more" {
		t.Fatalf("Japanese link label = %v, want visible text", got)
	}
	if got := fieldValue("商品ページのリンク先URL", anchor, "https://example.com/"); got != anchor.Href {
		t.Fatalf("Japanese URL field = %v, want %q", got, anchor.Href)
	}
}

func TestFieldValueReadsSemanticDataAttributes(t *testing.T) {
	node := browser.DOMNode{Tag: "div", Text: "Product", Attributes: map[string]string{
		"data-price": "$24.00",
		"data-sku":   "P-104",
	}}
	if got := fieldValue("price", node, "https://example.com/"); got != "$24.00" {
		t.Fatalf("price = %v, want data-price value", got)
	}
	if got := fieldValue("product SKU", node, "https://example.com/"); got != "P-104" {
		t.Fatalf("SKU = %v, want data-sku value", got)
	}
	if got := fieldValue("商品コード", node, "https://example.com/"); got != "P-104" {
		t.Fatalf("Japanese SKU = %v, want data-sku value", got)
	}
}
