package semantic

import (
	"context"
	"fmt"
	"sort"

	"github.com/longicorn/semcrawl/internal/browser"
	"github.com/longicorn/semcrawl/jev"
)

// Group every usable node, including non-landmark containers. Keep the DOM
// snapshot intact so field selection and ancestor traversal can reuse it.
func groupedCandidates(nodes []browser.DOMNode) []candidate {
	out := make([]candidate, 0, len(nodes))
	for _, node := range nodes {
		if eligibleSemanticNode(node) && (usefulNodeText(node) || node.DirectText != "") {
			out = append(out, candidate{node: node})
		}
	}
	return out
}

type nodeGroup struct {
	id    string
	nodes []candidate
}

func groupCandidates(candidates []candidate) []nodeGroup {
	groups := []nodeGroup{}
	indices := map[string]int{}
	for _, item := range candidates {
		key := item.node.Tag + "|" + normalizedClass(item.node.Class)
		index, ok := indices[key]
		if !ok {
			index = len(groups)
			indices[key] = index
			groups = append(groups, nodeGroup{id: fmt.Sprintf("g%d", index)})
		}
		groups[index].nodes = append(groups[index].nodes, item)
	}
	return groups
}

func (e *Extractor) findGroupedMatches(ctx context.Context, page browser.DOMSnapshot, target string, fields []Field, candidates []candidate) ([]candidate, jev.Usage, string, error) {
	groups := groupCandidates(candidates)
	var selected []candidate
	var total jev.Usage
	model := ""
	var batches [][]nodeGroup
	var requests []jev.Request
	for start := 0; start < len(groups); start += matchBatchSize {
		batch := groups[start:min(start+matchBatchSize, len(groups))]
		summaries := make([]map[string]any, 0, len(batch))
		questions := map[string]jev.Question{}
		for _, group := range batch {
			samples := []map[string]any{}
			for i := 0; i < min(3, len(group.nodes)); i++ {
				// Spread samples across the group rather than only its first nodes.
				index := i * (len(group.nodes) - 1) / max(1, min(3, len(group.nodes))-1)
				samples = append(samples, describeNode(group.nodes[index].node))
			}
			summaries = append(summaries, map[string]any{"id": group.id, "tag": group.nodes[0].node.Tag, "class": normalizedClass(group.nodes[0].node.Class), "count": len(group.nodes), "samples": samples})
			questions["group_"+group.id] = jev.Question{
				Type:         jev.QuestionNoul,
				Instructions: fmt.Sprintf("Could tag/class group %s contain individual records matching %q? Use the sample contents and attributes, not class names alone. Accept plausible groups for subsequent node verification; reject unrelated elements and whole-page/list wrappers.", group.id, target),
				Criteria:     map[string]any{"true": "This group plausibly contains requested records.", "false": "This group is unrelated or only contains wrappers."},
			}
		}
		requests = append(requests, jev.Request{State: map[string]any{"page": map[string]string{"title": page.Title}, "user_request": target, "groups": summaries}, Questions: questions})
		batches = append(batches, batch)
	}
	for index, result := range evaluateParallel(ctx, e.evaluator, requests) {
		if result.err != nil {
			return nil, total, model, fmt.Errorf("Jev group selection: %w", result.err)
		}
		response, batch := result.response, batches[index]
		addUsage(&total, response.Usage)
		if response.Model != "" {
			model = response.Model
		}
		for _, group := range batch {
			name := "group_" + group.id
			answer, ok := response.Answers[name]
			if !ok || answer.Type != jev.QuestionNoul || answer.Noul == nil {
				return nil, total, model, fmt.Errorf("Jev response is missing a valid answer for %s", name)
			}
			if *answer.Noul >= matchThreshold {
				selected = append(selected, group.nodes...)
			}
		}
	}
	// Every selected node is verified: sharing a class does not imply matching.
	matches, usage, currentModel, err := e.findMatches(ctx, page, target, selected)
	addUsage(&total, usage)
	if currentModel != "" {
		model = currentModel
	}
	if err != nil || len(matches) > 0 {
		return matches, total, model, err
	}
	matches, usage, currentModel, err = e.findFromFields(ctx, page, target, fields)
	addUsage(&total, usage)
	if currentModel != "" {
		model = currentModel
	}
	return matches, total, model, err
}

// Locate field anchors in bounded batches, then verify deduplicated ancestors
// starting with the nearest level. Never send one request per parent.
func (e *Extractor) findFromFields(ctx context.Context, page browser.DOMSnapshot, target string, fields []Field) ([]candidate, jev.Usage, string, error) {
	var leaves []browser.DOMNode
	for _, node := range page.Nodes {
		if len(selectFieldCandidates([]browser.DOMNode{node})) != 0 {
			leaves = append(leaves, node)
		}
	}
	byID := map[string]browser.DOMNode{}
	for _, node := range page.Nodes {
		byID[node.ID] = node
	}
	anchors := map[string]bool{}
	var total jev.Usage
	model := ""
	var batches [][]browser.DOMNode
	var requests []jev.Request
	for start := 0; start < len(leaves); start += matchBatchSize {
		batch := leaves[start:min(start+matchBatchSize, len(leaves))]
		summaries := []map[string]any{}
		questions := map[string]jev.Question{}
		for _, node := range batch {
			summaries = append(summaries, describeNode(node))
			questions["anchor_"+node.ID] = jev.Question{Type: jev.QuestionNoul, Instructions: fmt.Sprintf("Does element %s contain any requested field for a record matching %q? Use the field descriptions in state; reject unrelated page content.", node.ID, target), Criteria: map[string]any{"true": "This element contains a requested record field.", "false": "This element contains no relevant field."}}
		}
		requests = append(requests, jev.Request{State: map[string]any{"page": map[string]string{"title": page.Title}, "user_request": target, "fields": fields, "field_candidates": summaries}, Questions: questions})
		batches = append(batches, batch)
	}
	for index, result := range evaluateParallel(ctx, e.evaluator, requests) {
		if result.err != nil {
			return nil, total, model, fmt.Errorf("Jev field anchor selection: %w", result.err)
		}
		response, batch := result.response, batches[index]
		addUsage(&total, response.Usage)
		if response.Model != "" {
			model = response.Model
		}
		for _, node := range batch {
			name := "anchor_" + node.ID
			answer, ok := response.Answers[name]
			if !ok || answer.Type != jev.QuestionNoul || answer.Noul == nil {
				return nil, total, model, fmt.Errorf("Jev response is missing a valid answer for %s", name)
			}
			if *answer.Noul >= matchThreshold {
				anchors[node.ID] = true
			}
		}
	}
	frontier := []browser.DOMNode{}
	for _, node := range page.Nodes {
		if anchors[node.ID] {
			frontier = append(frontier, node)
		}
	}
	visited := map[string]bool{}
	var matches []candidate
	for len(frontier) > 0 {
		candidates := []candidate{}
		for _, node := range frontier {
			if visited[node.ID] {
				continue
			}
			visited[node.ID] = true
			if eligibleSemanticNode(node) {
				candidates = append(candidates, candidate{node: node})
			}
		}
		found, usage, currentModel, err := e.findMatches(ctx, page, target, candidates)
		addUsage(&total, usage)
		if currentModel != "" {
			model = currentModel
		}
		if err != nil {
			return nil, total, model, err
		}
		matches = append(matches, found...)
		matched := map[string]bool{}
		for _, item := range found {
			matched[item.node.ID] = true
		}
		next := []browser.DOMNode{}
		for _, node := range frontier {
			if matched[node.ID] {
				continue
			}
			if parent, ok := byID[node.ParentID]; ok && !visited[parent.ID] {
				next = append(next, parent)
			}
		}
		frontier = next
	}
	// Different anchors may reach the same record at different depths. Prefer
	// matched descendants over their matched list/page ancestors.
	matchedIDs := map[string]bool{}
	for _, item := range matches {
		matchedIDs[item.node.ID] = true
	}
	containers := map[string]bool{}
	for _, item := range matches {
		seen := map[string]bool{item.node.ID: true}
		for id := item.node.ParentID; id != "" && !seen[id]; {
			seen[id] = true
			if matchedIDs[id] {
				containers[id] = true
			}
			parent, ok := byID[id]
			if !ok {
				break
			}
			id = parent.ParentID
		}
	}
	out := make([]candidate, 0, len(matches))
	for _, item := range matches {
		if !containers[item.node.ID] {
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].node.Order < out[j].node.Order })
	return out, total, model, nil
}
