package main

import (
	"os"
	"strings"
	"testing"
)

func TestDocsWorkflowUsesScopedConcurrencyGroup(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/docs.yml")
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "group: pages-${{ github.event_name }}-${{ github.ref }}") {
		t.Fatalf("docs workflow must scope concurrency by event and ref")
	}
	if strings.Contains(content, "group: pages\n") {
		t.Fatalf("docs workflow must not use a global concurrency group")
	}
}

func TestDocsWorkflowSkipsPagesSetupOnPullRequests(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/docs.yml")
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "if: github.event_name != 'pull_request'") {
		t.Fatalf("docs workflow must skip Pages-only setup on pull requests")
	}
}

func TestNoSlopTaxonomyLivesInPublishedDocsSite(t *testing.T) {
	taxonomy, err := os.ReadFile("docs/src/content/docs/reference/slop-taxonomy.md")
	if err != nil {
		t.Fatalf("read published NoSlop taxonomy: %v", err)
	}
	if !strings.HasPrefix(string(taxonomy), "---\ntitle: NoSlop Taxonomy\n") {
		t.Fatalf("NoSlop taxonomy is missing docs-site front matter")
	}

	config, err := os.ReadFile("docs/astro.config.mjs")
	if err != nil {
		t.Fatalf("read docs config: %v", err)
	}
	if !strings.Contains(string(config), `slug: "reference/slop-taxonomy"`) {
		t.Fatalf("docs sidebar does not publish the NoSlop taxonomy")
	}

	reference, err := os.ReadFile("docs/src/content/docs/reference/repo-config.md")
	if err != nil {
		t.Fatalf("read repo config reference: %v", err)
	}
	if !strings.Contains(string(reference), "[NoSlop taxonomy](./slop-taxonomy/)") {
		t.Fatalf("repo config reference does not link to the published taxonomy")
	}
}

func TestNoSlopProvenanceDocsStateAdvisoryTrustBoundary(t *testing.T) {
	t.Parallel()

	want := "provenance conditioning is advisory until a trusted external system"
	for _, path := range []string{"README.md", "docs/src/content/docs/reference/repo-config.md"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(strings.ToLower(string(content)), want) {
			t.Errorf("%s does not state the caller-controlled provenance trust boundary", path)
		}
	}
}
