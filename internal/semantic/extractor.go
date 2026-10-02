package semantic

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/longicorn/semcrawl/internal/browser"
	"github.com/longicorn/semcrawl/jev"
)

const (
	matchBatchSize     = 20
	fieldBatchSize     = 5
	matchThreshold     = 0.65
	maxFields          = 12
	maxChildren        = 30
	DefaultConcurrency = 2
	MaxConcurrency     = 8
)

var ErrNoCandidates = errors.New("no usable DOM elements found on this page")

type Evaluator interface {
	Evaluate(context.Context, jev.Request) (*jev.Response, error)
}

type Field struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Match struct {
	NodeID string         `json:"node_id"`
	Score  float64        `json:"score"`
	Values map[string]any `json:"values"`
}

type Result struct {
	Target string    `json:"target"`
	URL    string    `json:"url"`
	Title  string    `json:"title"`
	Items  []Match   `json:"items"`
	Model  string    `json:"model,omitempty"`
	Usage  jev.Usage `json:"usage"`
}

type candidate struct {
	node     browser.DOMNode
	repeated bool
	score    float64
}

type fieldWork struct {
	candidates []browser.DOMNode
}

type Extractor struct {
	evaluator   Evaluator
	concurrency int
}

func NewExtractor(evaluator Evaluator) *Extractor {
	return NewExtractorWithConcurrency(evaluator, DefaultConcurrency)
}

func NewExtractorWithConcurrency(evaluator Evaluator, concurrency int) *Extractor {
	if concurrency < 1 || concurrency > MaxConcurrency {
		concurrency = DefaultConcurrency
	}
	return &Extractor{evaluator: evaluator, concurrency: concurrency}
}

func ValidateConcurrency(concurrency int) error {
	if concurrency < 1 || concurrency > MaxConcurrency {
		return fmt.Errorf("Jev concurrency must be between 1 and %d", MaxConcurrency)
	}
	return nil
}

// Extract uses Jev to find elements matching target, then selects each
// requested field from the matching element's descendants.
func (e *Extractor) Extract(ctx context.Context, page browser.DOMSnapshot, target string, fields []Field, limit int) (*Result, error) {
	if err := Validate(target, fields, limit); err != nil {
		return nil, err
	}
	target = strings.TrimSpace(target)

	candidates := groupedCandidates(page.Nodes)
	if len(candidates) == 0 {
		return nil, ErrNoCandidates
	}
	matches, usage, model, err := e.findGroupedMatches(ctx, page, target, fields, candidates)
	if err != nil {
		return nil, err
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	result := &Result{Target: target, URL: page.URL, Title: page.Title, Items: make([]Match, 0, len(matches)), Usage: usage, Model: model}
	for _, match := range matches {
		result.Items = append(result.Items, Match{NodeID: match.node.ID, Score: match.score, Values: make(map[string]any, len(fields))})
	}
	if len(matches) == 0 {
		return result, nil
	}
	fieldUsage, fieldModel, err := e.extractFieldsFromRepresentative(ctx, page, target, fields, matches, result.Items)
	if err != nil {
		return nil, err
	}
	result.Usage.InputTokens += fieldUsage.InputTokens
	result.Usage.OutputTokens += fieldUsage.OutputTokens
	if fieldModel != "" {
		result.Model = fieldModel
	}
	return result, nil
}

func addUsage(total *jev.Usage, usage jev.Usage) {
	total.InputTokens += usage.InputTokens
	total.OutputTokens += usage.OutputTokens
}

func Validate(target string, fields []Field, limit int) error {
	if strings.TrimSpace(target) == "" {
		return errors.New("target description is required")
	}
	if limit < 1 || limit > 100 {
		return errors.New("limit must be between 1 and 100")
	}
	if len(fields) == 0 || len(fields) > maxFields {
		return fmt.Errorf("provide between 1 and %d fields", maxFields)
	}
	seen := make(map[string]bool)
	for _, field := range fields {
		name := strings.TrimSpace(field.Name)
		if name == "" || strings.TrimSpace(field.Description) == "" {
			return errors.New("each field needs a name and natural language description")
		}
		if seen[name] {
			return fmt.Errorf("duplicate field name %q", name)
		}
		seen[name] = true
	}
	return nil
}

func (e *Extractor) findMatches(ctx context.Context, page browser.DOMSnapshot, target string, candidates []candidate, prior ...any) ([]candidate, jev.Usage, string, error) {
	var matches []candidate
	var usage jev.Usage
	model := ""
	if len(prior) > 0 {
		usage = prior[0].(jev.Usage)
		model = prior[1].(string)
	}
	var batches [][]candidate
	var requests []jev.Request
	for start := 0; start < len(candidates); start += matchBatchSize {
		end := min(start+matchBatchSize, len(candidates))
		batch := candidates[start:end]
		stateCandidates := make([]map[string]any, 0, len(batch))
		questions := make(map[string]jev.Question, len(batch))
		for _, item := range batch {
			stateCandidates = append(stateCandidates, describeNode(item.node))
			questionName := "match_" + item.node.ID
			questions[questionName] = jev.Question{
				Type:         jev.QuestionNoul,
				Instructions: fmt.Sprintf("The user's scraping request is to find %q. Decide whether DOM element %s represents one matching result that should be returned as a record. Answer yes for a matching record, and no for a wrapper, unrelated element, or non-record content.", target, item.node.ID),
				Criteria:     map[string]any{"true": "This element is one result matching the user's requested target.", "false": "This element is only a wrapper or does not match the requested target."},
			}
		}
		requests = append(requests, jev.Request{
			State: map[string]any{
				"page":         map[string]string{"title": page.Title},
				"user_request": target,
				"candidates":   stateCandidates,
			},
			Questions: questions,
		})
		batches = append(batches, batch)
	}
	for index, result := range evaluateParallel(ctx, e.evaluator, e.concurrency, requests) {
		if result.err != nil {
			return nil, usage, model, fmt.Errorf("Jev target matching: %w", result.err)
		}
		response, batch := result.response, batches[index]
		addUsage(&usage, response.Usage)
		if response.Model != "" {
			model = response.Model
		}
		for _, item := range batch {
			name := "match_" + item.node.ID
			answer, ok := response.Answers[name]
			if !ok || answer.Type != jev.QuestionNoul || answer.Noul == nil {
				return nil, usage, model, fmt.Errorf("Jev response is missing a valid answer for %s", name)
			}
			if *answer.Noul >= matchThreshold {
				item.score = *answer.Noul
				matches = append(matches, item)
			}
		}
	}
	if repeatedCount(matches) > 1 {
		filtered := matches[:0]
		for _, match := range matches {
			if match.repeated {
				filtered = append(filtered, match)
			}
		}
		matches = filtered
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].node.Order < matches[j].node.Order })
	return matches, usage, model, nil
}

func (e *Extractor) extractFields(ctx context.Context, page browser.DOMSnapshot, target string, fields []Field, matches []candidate, output []Match) (jev.Usage, string, error) {
	children := childIndex(page.Nodes)
	var totalUsage jev.Usage
	model := ""
	for start := 0; start < len(matches); start += fieldBatchSize {
		end := min(start+fieldBatchSize, len(matches))
		batch := matches[start:end]
		stateItems := make([]map[string]any, 0, len(batch))
		works := make(map[string]fieldWork)
		questions := make(map[string]jev.Question, len(batch)*len(fields))
		for _, match := range batch {
			descendants := descendants(match.node.ID, children)
			if match.node.DirectText != "" || match.node.Href != "" || match.node.Src != "" || match.node.Alt != "" || match.node.AriaLabel != "" || match.node.Title != "" || len(match.node.Attributes) > 0 {
				descendants = append([]browser.DOMNode{match.node}, descendants...)
			}
			fieldCandidates := selectFieldCandidates(descendants)
			works[match.node.ID] = fieldWork{candidates: fieldCandidates}
			choiceNodes := make([]map[string]any, 0, len(fieldCandidates))
			options := make(map[string]any, len(fieldCandidates)+1)
			for _, node := range fieldCandidates {
				choiceNodes = append(choiceNodes, describeNode(node))
				options[node.ID] = formatNode(node)
			}
			options["none"] = "No element in this item contains the requested field."
			stateItems = append(stateItems, map[string]any{
				"item":             map[string]any{"id": match.node.ID, "element": describeNode(match.node)},
				"field_candidates": choiceNodes,
			})
			for fieldIndex, field := range fields {
				name := fieldQuestionName(match.node.ID, fieldIndex)
				questions[name] = jev.Question{
					Type:         jev.QuestionChoice,
					Instructions: fmt.Sprintf("For item %s found as %q, select the one descendant DOM element that best contains the requested field %q. Consider text and attributes such as href or src. Choose none if no candidate matches.", match.node.ID, target, field.Description),
					Criteria:     options,
				}
			}
		}
		response, err := e.evaluator.Evaluate(ctx, jev.Request{
			State: map[string]any{
				"page":         map[string]string{"title": page.Title},
				"user_request": target,
				"items":        stateItems,
			},
			Questions: questions,
		})
		if err != nil {
			return totalUsage, model, fmt.Errorf("Jev field selection: %w", err)
		}
		totalUsage.InputTokens += response.Usage.InputTokens
		totalUsage.OutputTokens += response.Usage.OutputTokens
		model = response.Model
		for offset, match := range batch {
			itemIndex := start + offset
			work := works[match.node.ID]
			byID := make(map[string]browser.DOMNode, len(work.candidates))
			for _, node := range work.candidates {
				byID[node.ID] = node
			}
			for fieldIndex, field := range fields {
				name := fieldQuestionName(match.node.ID, fieldIndex)
				answer, ok := response.Answers[name]
				if !ok || answer.Type != jev.QuestionChoice || answer.Choice == "" {
					return totalUsage, model, fmt.Errorf("Jev response is missing a valid answer for %s", name)
				}
				if answer.Choice == "none" {
					storeFieldValue(output[itemIndex].Values, fields, field, nil, match.node.ID, byID, children, page.URL)
					continue
				}
				value, ok := byID[answer.Choice]
				if !ok {
					return totalUsage, model, fmt.Errorf("Jev selected unknown DOM candidate %q for %s", answer.Choice, name)
				}
				storeFieldValue(output[itemIndex].Values, fields, field, &value, match.node.ID, byID, children, page.URL)
			}
		}
	}
	return totalUsage, model, nil
}

type fieldSelector struct {
	field Field
	node  browser.DOMNode
	rank  int
}

func (e *Extractor) extractFieldsFromRepresentative(ctx context.Context, page browser.DOMSnapshot, target string, fields []Field, matches []candidate, output []Match) (jev.Usage, string, error) {
	selectors, usage, model, err := e.selectRepresentativeFields(ctx, page, target, fields, matches[0].node, output[0])
	if err != nil {
		return usage, model, err
	}
	children := childIndex(page.Nodes)
	byID := nodeIndex(page.Nodes)
	for itemIndex := 1; itemIndex < len(matches); itemIndex++ {
		for _, selector := range selectors {
			node, ok := findFieldNode(matches[itemIndex].node.ID, selector, children)
			if !ok {
				storeFieldValue(output[itemIndex].Values, fields, selector.field, nil, matches[itemIndex].node.ID, byID, children, page.URL)
				continue
			}
			storeFieldValue(output[itemIndex].Values, fields, selector.field, &node, matches[itemIndex].node.ID, byID, children, page.URL)
		}
	}
	return usage, model, nil
}

func (e *Extractor) selectRepresentativeFields(ctx context.Context, page browser.DOMSnapshot, target string, fields []Field, representative browser.DOMNode, output Match) ([]fieldSelector, jev.Usage, string, error) {
	children := childIndex(page.Nodes)
	byID := nodeIndex(page.Nodes)
	descendants := descendants(representative.ID, children)
	if representative.DirectText != "" || representative.Href != "" || representative.Src != "" || representative.Alt != "" || representative.AriaLabel != "" || representative.Title != "" || len(representative.Attributes) > 0 {
		descendants = append([]browser.DOMNode{representative}, descendants...)
	}
	fieldCandidates := selectFieldCandidates(descendants)
	choiceNodes := make([]map[string]any, 0, len(fieldCandidates))
	options := make(map[string]any, len(fieldCandidates)+1)
	for _, node := range fieldCandidates {
		choiceNodes = append(choiceNodes, describeNode(node))
		options[node.ID] = formatNode(node)
	}
	options["none"] = "No element in this item contains the requested field."
	questions := make(map[string]jev.Question, len(fields))
	for index, field := range fields {
		questions[fmt.Sprintf("field_%s_%d", representative.ID, index)] = jev.Question{
			Type:         jev.QuestionChoice,
			Instructions: fmt.Sprintf("For the representative record found as %q, select the descendant DOM element that contains the requested field %q. Choose none if unavailable.", target, field.Description),
			Criteria:     options,
		}
	}
	response, err := e.evaluator.Evaluate(ctx, jev.Request{
		State:     map[string]any{"page": map[string]string{"title": page.Title}, "user_request": target, "item": describeNode(representative), "field_candidates": choiceNodes},
		Questions: questions,
	})
	if err != nil {
		return nil, jev.Usage{}, "", fmt.Errorf("Jev representative field selection: %w", err)
	}
	candidateByID := make(map[string]browser.DOMNode, len(fieldCandidates))
	for _, node := range fieldCandidates {
		candidateByID[node.ID] = node
	}
	selectors := make([]fieldSelector, 0, len(fields))
	for index, field := range fields {
		name := fmt.Sprintf("field_%s_%d", representative.ID, index)
		answer, ok := response.Answers[name]
		if !ok || answer.Type != jev.QuestionChoice || answer.Choice == "" {
			return nil, response.Usage, response.Model, fmt.Errorf("Jev response is missing a valid answer for %s", name)
		}
		if answer.Choice == "none" {
			storeFieldValue(output.Values, fields, field, nil, representative.ID, byID, children, page.URL)
			continue
		}
		chosen, ok := candidateByID[answer.Choice]
		if !ok {
			return nil, response.Usage, response.Model, fmt.Errorf("Jev selected unknown DOM candidate %q for %s", answer.Choice, name)
		}
		storeFieldValue(output.Values, fields, field, &chosen, representative.ID, byID, children, page.URL)
		rank := 0
		for _, sibling := range fieldCandidates {
			if sibling.Tag == chosen.Tag && normalizedClass(sibling.Class) == normalizedClass(chosen.Class) {
				if sibling.ID == chosen.ID {
					break
				}
				rank++
			}
		}
		selectors = append(selectors, fieldSelector{field: field, node: chosen, rank: rank})
	}
	return selectors, response.Usage, response.Model, nil
}

func findFieldNode(itemID string, selector fieldSelector, children map[string][]browser.DOMNode) (browser.DOMNode, bool) {
	descendants := descendants(itemID, children)
	var samePattern []browser.DOMNode
	for _, node := range descendants {
		if node.Tag == selector.node.Tag && normalizedClass(node.Class) == normalizedClass(selector.node.Class) {
			samePattern = append(samePattern, node)
		}
	}
	if selector.rank < len(samePattern) {
		return samePattern[selector.rank], true
	}
	// On markup with per-item class variation, reuse the tag and pick its
	// occurrence rank among descendants.
	var sameTag []browser.DOMNode
	for _, node := range descendants {
		if node.Tag == selector.node.Tag {
			sameTag = append(sameTag, node)
		}
	}
	if selector.rank < len(sameTag) {
		return sameTag[selector.rank], true
	}
	return browser.DOMNode{}, false
}

func makeCandidates(nodes []browser.DOMNode, limit int) []candidate {
	byParent := make(map[string][]browser.DOMNode)
	for _, node := range nodes {
		byParent[node.ParentID] = append(byParent[node.ParentID], node)
	}
	type groupKey struct{ parent, signature string }
	groups := make(map[groupKey][]browser.DOMNode)
	for _, node := range nodes {
		if !eligibleSemanticNode(node) || !usefulNodeText(node) {
			continue
		}
		key := groupKey{node.ParentID, node.Tag + "|" + node.Role + "|" + normalizedClass(node.Class)}
		groups[key] = append(groups[key], node)
	}
	repeatedIDs := make(map[string]bool)
	for _, group := range groups {
		if len(group) >= 2 {
			for _, node := range group {
				repeatedIDs[node.ID] = true
			}
		}
	}
	out := make([]candidate, 0, limit)
	added := make(map[string]bool)
	add := func(node browser.DOMNode, repeated bool) {
		if len(out) >= limit || added[node.ID] || !eligibleSemanticNode(node) || !usefulNodeText(node) {
			return
		}
		added[node.ID] = true
		out = append(out, candidate{node: node, repeated: repeated})
	}
	// Repeating siblings are likely records such as product cards or search results.
	for _, node := range nodes {
		if repeatedIDs[node.ID] {
			add(node, true)
		}
	}
	// Add semantic landmarks and text leaves for pages without repeated records.
	for _, node := range nodes {
		if isLandmark(node) || isTextLeaf(node, byParent) {
			add(node, false)
		}
	}
	return out
}

func repeatedCount(matches []candidate) int {
	count := 0
	for _, match := range matches {
		if match.repeated {
			count++
		}
	}
	return count
}

func normalizedClass(class string) string {
	parts := strings.Fields(class)
	sort.Strings(parts)
	return strings.Join(parts, ".")
}

func isLandmark(node browser.DOMNode) bool {
	if !eligibleSemanticNode(node) {
		return false
	}
	switch node.Tag {
	case "main", "article", "section", "li", "tr", "h1", "h2", "h3", "blockquote", "a", "button":
		return true
	}
	switch node.Role {
	case "main", "article", "listitem", "row":
		return true
	}
	return false
}

func isTextLeaf(node browser.DOMNode, children map[string][]browser.DOMNode) bool {
	if len(strings.TrimSpace(node.DirectText)) < 4 || len(node.DirectText) > 300 {
		return false
	}
	for _, child := range children[node.ID] {
		if strings.TrimSpace(child.Text) != "" {
			return false
		}
	}
	return eligibleSemanticNode(node)
}

// eligibleSemanticNode excludes elements that rarely carry useful scraping
// records or fields. The page snapshot itself remains intact for `content`.
func eligibleSemanticNode(node browser.DOMNode) bool {
	switch strings.ToLower(node.Tag) {
	case "script", "style", "noscript", "template", "font", "center", "br", "hr", "wbr":
		return false
	default:
		return true
	}
}

func usefulNodeText(node browser.DOMNode) bool {
	if !eligibleSemanticNode(node) {
		return false
	}
	text := strings.TrimSpace(node.Text)
	if len(text) >= 12 {
		return true
	}
	if node.Href != "" || node.Src != "" || node.Alt != "" || node.AriaLabel != "" || len(node.Attributes) > 0 {
		return true
	}
	switch node.Tag {
	case "main", "article", "section", "li", "tr", "h1", "h2", "h3", "blockquote", "a", "button":
		return text != ""
	}
	return node.Role != "" && text != ""
}

func childIndex(nodes []browser.DOMNode) map[string][]browser.DOMNode {
	children := make(map[string][]browser.DOMNode)
	for _, node := range nodes {
		children[node.ParentID] = append(children[node.ParentID], node)
	}
	for parent := range children {
		sort.Slice(children[parent], func(i, j int) bool { return children[parent][i].Order < children[parent][j].Order })
	}
	return children
}

func nodeIndex(nodes []browser.DOMNode) map[string]browser.DOMNode {
	byID := make(map[string]browser.DOMNode, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	return byID
}

func storeFieldValue(values map[string]any, fields []Field, field Field, selected *browser.DOMNode, recordID string, byID map[string]browser.DOMNode, children map[string][]browser.DOMNode, baseURL string) {
	if selected == nil {
		values[field.Name] = nil
	} else {
		values[field.Name] = fieldValue(field.Description, *selected, baseURL)
	}
	if key := companionURLKey(field, fields); key != "" {
		if selected == nil {
			values[key] = nil
			return
		}
		if href := associatedLinkURL(recordID, *selected, byID, children); href != "" {
			values[key] = absoluteURL(baseURL, href)
		} else {
			values[key] = nil
		}
	}
}

func companionURLKey(field Field, fields []Field) string {
	if !containsAny(strings.ToLower(field.Name+" "+field.Description), "name", "title", "名前", "名称", "物件名", "商品名", "掲載名", "タイトル", "題名") {
		return ""
	}
	key := field.Name + "_url"
	for _, requested := range fields {
		if requested.Name == key {
			return ""
		}
	}
	return key
}

// associatedLinkURL finds a link on the selected name node, inside it, or on
// its nearest enclosing anchor. It deliberately stays within the matched
// record and never follows the destination.
func associatedLinkURL(recordID string, selected browser.DOMNode, byID map[string]browser.DOMNode, children map[string][]browser.DOMNode) string {
	if selected.Href != "" {
		return selected.Href
	}
	var firstAnchor string
	for _, child := range descendants(selected.ID, children) {
		if child.Tag != "a" || child.Href == "" {
			continue
		}
		if firstAnchor == "" {
			firstAnchor = child.Href
		}
		if linkTextMatches(selected.Text, child.Text) || linkTextMatches(selected.Text, child.DirectText) {
			return child.Href
		}
	}
	if firstAnchor != "" {
		return firstAnchor
	}
	for id := selected.ParentID; id != ""; {
		parent, ok := byID[id]
		if !ok {
			break
		}
		if parent.Tag == "a" && parent.Href != "" {
			return parent.Href
		}
		if id == recordID {
			break
		}
		id = parent.ParentID
	}
	return ""
}

func linkTextMatches(fieldText, linkText string) bool {
	fieldText = strings.Join(strings.Fields(fieldText), " ")
	linkText = strings.Join(strings.Fields(linkText), " ")
	return len(fieldText) >= 2 && (fieldText == linkText || strings.Contains(linkText, fieldText) || strings.Contains(fieldText, linkText))
}

func descendants(id string, children map[string][]browser.DOMNode) []browser.DOMNode {
	out := make([]browser.DOMNode, 0, maxChildren)
	var walk func(string)
	walk = func(parent string) {
		for _, child := range children[parent] {
			if len(out) >= maxChildren {
				return
			}
			out = append(out, child)
			walk(child.ID)
		}
	}
	walk(id)
	return out
}

func selectFieldCandidates(nodes []browser.DOMNode) []browser.DOMNode {
	out := make([]browser.DOMNode, 0, maxChildren)
	for _, node := range nodes {
		if !eligibleSemanticNode(node) {
			continue
		}
		if node.DirectText == "" && node.Href == "" && node.Src == "" && node.Alt == "" && node.Title == "" && node.AriaLabel == "" && len(node.Attributes) == 0 {
			continue
		}
		out = append(out, node)
		if len(out) == maxChildren {
			break
		}
	}
	return out
}

func describeNode(node browser.DOMNode) map[string]any {
	return map[string]any{
		"id": node.ID, "tag": node.Tag, "role": trim(node.Role, 80), "class": trim(node.Class, 120),
		"aria_label": trim(node.AriaLabel, 160), "text": trim(node.Text, 320), "direct_text": trim(node.DirectText, 200),
		"href": trim(node.Href, 300), "src": trim(node.Src, 300), "alt": trim(node.Alt, 160), "title": trim(node.Title, 160),
		"attributes": node.Attributes,
	}
}

func formatNode(node browser.DOMNode) string {
	parts := []string{node.Tag}
	if node.Role != "" {
		parts = append(parts, "role="+node.Role)
	}
	if node.AriaLabel != "" {
		parts = append(parts, "label="+node.AriaLabel)
	}
	if node.Text != "" {
		parts = append(parts, "text="+node.Text)
	}
	if node.Href != "" {
		parts = append(parts, "href="+node.Href)
	}
	if node.Src != "" {
		parts = append(parts, "src="+node.Src)
	}
	if node.Alt != "" {
		parts = append(parts, "alt="+node.Alt)
	}
	keys := make([]string, 0, len(node.Attributes))
	for key := range node.Attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := node.Attributes[key]
		parts = append(parts, key+"="+value)
	}
	return strings.Join(parts, "; ")
}

func fieldQuestionName(nodeID string, fieldIndex int) string {
	return fmt.Sprintf("field_%s_%d", nodeID, fieldIndex)
}

func fieldValue(description string, node browser.DOMNode, baseURL string) any {
	field := strings.ToLower(description)
	switch {
	case wantsLinkURL(field) && node.Href != "":
		return absoluteURL(baseURL, node.Href)
	case containsAny(field, "alt", "image description", "代替テキスト", "画像の説明", "画像説明") && node.Alt != "":
		return node.Alt
	case containsAny(field, "image", "photo", "picture", "src", "画像", "写真") && node.Src != "":
		return absoluteURL(baseURL, node.Src)
	}
	if value, ok := matchingAttribute(field, node.Attributes); ok {
		return value
	}
	switch {
	case node.DirectText != "":
		return node.DirectText
	case node.AriaLabel != "":
		return node.AriaLabel
	case node.Title != "":
		return node.Title
	case node.Alt != "":
		return node.Alt
	case node.Href != "":
		return absoluteURL(baseURL, node.Href)
	case node.Src != "":
		return absoluteURL(baseURL, node.Src)
	default:
		return node.Text
	}
}

func matchingAttribute(field string, attributes map[string]string) (string, bool) {
	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		keys = append(keys, strings.ToLower(key))
	}
	sort.Strings(keys)
	for _, key := range keys {
		name := strings.TrimPrefix(key, "data-")
		name = strings.ReplaceAll(name, "-", " ")
		terms := strings.Fields(name)
		if len(terms) == 0 {
			continue
		}
		matched := true
		for _, term := range terms {
			if term == "product" || term == "item" {
				continue
			}
			if !containsAny(field, term) && !containsJapaneseAttributeTerm(field, term) {
				matched = false
				break
			}
		}
		if matched {
			for originalKey, value := range attributes {
				if strings.EqualFold(originalKey, key) {
					return value, true
				}
			}
		}
	}
	return "", false
}

func containsJapaneseAttributeTerm(field, term string) bool {
	aliases := map[string][]string{
		"price":        {"価格", "値段"},
		"sku":          {"商品コード", "型番"},
		"id":           {"商品番号", "商品id", "識別子"},
		"rating":       {"評価", "レビュー"},
		"availability": {"在庫", "販売状況"},
		"stock":        {"在庫", "在庫数"},
		"currency":     {"通貨"},
		"date":         {"日付", "公開日", "更新日"},
		"datetime":     {"日時", "公開日", "更新日"},
		"url":          {"url", "リンク先"},
		"name":         {"名前", "商品名"},
		"title":        {"タイトル", "題名"},
	}
	for _, alias := range aliases[term] {
		if strings.Contains(field, alias) {
			return true
		}
	}
	return false
}

func wantsLinkURL(field string) bool {
	if containsAny(field, "text", "label", "name", "title", "caption", "description", "alt", "表示", "文字", "名前", "タイトル", "ラベル", "テキスト", "説明", "本文") {
		return false
	}
	return containsAny(field, "url", "href", "destination", "link", "リンク", "遷移先")
}

func containsAny(value string, options ...string) bool {
	for _, option := range options {
		if strings.Contains(value, option) {
			return true
		}
	}
	return false
}

func absoluteURL(base, value string) string {
	baseURL, err := url.Parse(base)
	if err != nil {
		return value
	}
	ref, err := url.Parse(value)
	if err != nil {
		return value
	}
	return baseURL.ResolveReference(ref).String()
}

func trim(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
