package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/slop/lenses"
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
	// A Windows checkout under core.autocrlf=true materializes the doc with
	// CRLF, so a line-spanning assertion has to read the line endings as
	// formatting rather than content.
	if !strings.HasPrefix(normalizeEOL(string(taxonomy)), "---\ntitle: NoSlop Taxonomy\n") {
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

func normalizeEOL(content string) string {
	return strings.ReplaceAll(content, "\r\n", "\n")
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

// TestEveryMarkdownDocLivesInThePublishedContentRoot generalises a finding that
// was fixed once by hand and then reappeared. A reference page under docs/ but
// outside the Starlight content root is invisible on the published site while
// still looking published in the repository, and linking to it from README
// makes it read as documentation rather than as a stray file.
func TestEveryMarkdownDocLivesInThePublishedContentRoot(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir("docs")
	if err != nil {
		t.Fatalf("read docs dir: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		t.Errorf("docs/%s sits outside docs/src/content/docs and will not publish", entry.Name())
	}
}

// TestPublishedDocLinksAreNotRootAbsolute pins the other half. The site is
// served under a base path, so a root-absolute link resolves above it and 404s
// on the published site while working in a local preview.
func TestPublishedDocLinksAreNotRootAbsolute(t *testing.T) {
	t.Parallel()

	root := filepath.Join("docs", "src", "content", "docs")
	rootAbsolute := regexp.MustCompile(`\]\(/(?:reference|guides|concepts|start-here)/`)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for index, line := range strings.Split(normalizeEOL(string(content)), "\n") {
			if rootAbsolute.MatchString(line) {
				t.Errorf("%s:%d uses a root-absolute link, which 404s under the site base path", path, index+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk published docs: %v", err)
	}
}

// TestPublishedTaxonomyNamesEveryLens is the parity check the taxonomy never
// had. The catalog and the published explanation agreed by hand; nothing made
// them agree, so adding a lens could ship a name the docs never mention.
func TestPublishedTaxonomyNamesEveryLens(t *testing.T) {
	t.Parallel()

	taxonomy, err := os.ReadFile("docs/src/content/docs/reference/slop-taxonomy.md")
	if err != nil {
		t.Fatalf("read published NoSlop taxonomy: %v", err)
	}
	published := normalizeEOL(string(taxonomy))
	for _, name := range lenses.Names() {
		if !strings.Contains(published, name) {
			t.Errorf("lens %q is in the catalog but not in the published taxonomy", name)
		}
	}
}
