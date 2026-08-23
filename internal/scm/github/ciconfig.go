package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/scm"
	"gopkg.in/yaml.v3"
)

// ProbeCIConfiguration determines whether a workflow in the exact commit tree
// has an automatic trigger capable of reporting on this PR head. Merely having
// a workflow file is insufficient: schedule/manual/default-only workflows can
// never end an empty-check wait for a feature PR.
func (h *Host) ProbeCIConfiguration(ctx context.Context, _ *scm.PR, branch, baseBranch, headSHA string) (scm.CIConfiguration, error) {
	slug := h.apiRepoSlug()
	if slug == "" {
		return scm.CIConfigurationUnknown, errors.New("no GitHub repository is known; cannot determine whether CI is configured")
	}
	entries, absent, err := h.workflowEntries(ctx, slug, headSHA)
	if err != nil {
		return scm.CIConfigurationUnknown, err
	}
	if absent {
		return scm.CIConfigurationAbsent, nil
	}
	for _, entry := range entries {
		if entry.Type != "" && entry.Type != "file" || !isWorkflowDefinition(entry.Name) {
			continue
		}
		content, err := h.workflowContent(ctx, slug, entry.Path, headSHA)
		if err != nil {
			return scm.CIConfigurationUnknown, err
		}
		canRun, err := workflowCanReportOnPRHead(content, branch, baseBranch)
		if err != nil {
			return scm.CIConfigurationUnknown, fmt.Errorf("parse workflow %s: %w", entry.Path, err)
		}
		if canRun {
			return scm.CIConfigurationPresent, nil
		}
	}
	return scm.CIConfigurationAbsent, nil
}

type workflowEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
}

func (h *Host) workflowEntries(ctx context.Context, slug, headSHA string) ([]workflowEntry, bool, error) {
	path := "repos/" + slug + "/contents/.github/workflows"
	if strings.TrimSpace(headSHA) != "" {
		path += "?ref=" + url.QueryEscape(headSHA)
	}
	out, err := h.cmd(ctx, "gh", h.apiArgs(path)...).CombinedOutput()
	if err != nil {
		if isNotFound(out) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("gh api contents/.github/workflows: %s: %w", strings.TrimSpace(string(out)), err)
	}
	var entries []workflowEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, false, fmt.Errorf("parse workflow directory: %w", err)
	}
	return entries, false, nil
}

func (h *Host) workflowContent(ctx context.Context, slug, workflowPath, headSHA string) ([]byte, error) {
	path := "repos/" + slug + "/contents/" + strings.TrimPrefix(workflowPath, "/")
	if strings.TrimSpace(headSHA) != "" {
		path += "?ref=" + url.QueryEscape(headSHA)
	}
	out, err := h.cmd(ctx, "gh", h.apiArgs(path)...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh api workflow content: %s: %w", strings.TrimSpace(string(out)), err)
	}
	var payload struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("parse workflow content: %w", err)
	}
	if payload.Encoding != "base64" {
		return nil, fmt.Errorf("workflow content has unsupported encoding %q", payload.Encoding)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(payload.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("decode workflow content: %w", err)
	}
	return decoded, nil
}

func (h *Host) apiRepoSlug() string {
	parts := strings.Split(strings.Trim(strings.TrimSpace(h.repo), "/"), "/")
	switch len(parts) {
	case 2:
		return parts[0] + "/" + parts[1]
	case 3:
		return parts[1] + "/" + parts[2]
	default:
		return ""
	}
}

func (h *Host) apiArgs(path string) []string {
	args := []string{"api"}
	if h.host != "" {
		args = append(args, "--hostname", h.host)
	}
	return append(args, path)
}

func workflowCanReportOnPRHead(content []byte, branch, baseBranch string) (bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return false, err
	}
	root := documentMapping(&doc)
	if root == nil {
		return false, errors.New("workflow root is not a mapping")
	}
	on := mappingValue(root, "on")
	if on == nil {
		return false, nil
	}
	switch on.Kind {
	case yaml.ScalarNode:
		return automaticEventCanRun(on.Value, nil, branch, baseBranch), nil
	case yaml.SequenceNode:
		for _, event := range on.Content {
			if event.Kind == yaml.ScalarNode && automaticEventCanRun(event.Value, nil, branch, baseBranch) {
				return true, nil
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(on.Content); i += 2 {
			if automaticEventCanRun(on.Content[i].Value, on.Content[i+1], branch, baseBranch) {
				return true, nil
			}
		}
	default:
		return false, errors.New("workflow on value has unsupported shape")
	}
	return false, nil
}

func documentMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	return doc
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func automaticEventCanRun(event string, config *yaml.Node, branch, baseBranch string) bool {
	switch strings.TrimSpace(event) {
	case "pull_request":
		return triggerAllowsBranch(config, baseBranch)
	case "push":
		return triggerAllowsBranch(config, branch)
	default:
		return false
	}
}

func triggerAllowsBranch(config *yaml.Node, branch string) bool {
	if config == nil || config.Kind == yaml.ScalarNode && (config.Tag == "!!null" || config.Value == "") {
		return true
	}
	if config.Kind != yaml.MappingNode {
		return true
	}
	branches := stringList(mappingValue(config, "branches"))
	ignored := stringList(mappingValue(config, "branches-ignore"))
	// A tags-only push trigger opts out of branch pushes.
	if len(branches) == 0 && len(ignored) == 0 && (mappingValue(config, "tags") != nil || mappingValue(config, "tags-ignore") != nil) {
		return false
	}
	if len(branches) > 0 && !orderedPatternMatch(branches, branch) {
		return false
	}
	return !orderedPatternMatch(ignored, branch)
}

func stringList(node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.ScalarNode {
		return []string{node.Value}
	}
	var values []string
	if node.Kind == yaml.SequenceNode {
		for _, item := range node.Content {
			if item.Kind == yaml.ScalarNode {
				values = append(values, item.Value)
			}
		}
	}
	return values
}

func orderedPatternMatch(patterns []string, value string) bool {
	matched := false
	for _, pattern := range patterns {
		negative := strings.HasPrefix(pattern, "!")
		pattern = strings.TrimPrefix(pattern, "!")
		if githubGlobMatch(pattern, value) {
			matched = !negative
		}
	}
	return matched
}

func githubGlobMatch(pattern, value string) bool {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String()).MatchString(value)
}

func isWorkflowDefinition(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")
}

func isNotFound(out []byte) bool { return strings.Contains(string(out), "HTTP 404") }
