package lenses_test

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/slop/lenses"
)

func TestCatalogDefinesEveryV1LensForReviewerPrompts(t *testing.T) {
	t.Parallel()

	want := []string{
		"vacuous-check",
		"test-capitulation",
		"self-consistent-oracle",
		"comment-defended-workaround",
		"scope-expansion",
		"asserted-followup-without-artifact",
		"fail-open-default",
		"rule-applied-in-one-place-not-sibling",
		"redundant-comment",
	}
	catalog := lenses.Catalog()
	names := lenses.Names()
	if len(catalog) != len(want) {
		t.Fatalf("catalog has %d lenses, want %d", len(catalog), len(want))
	}

	prompt := lenses.ReviewerPrompt()
	for i, name := range want {
		lens := catalog[i]
		if lens.Name != name {
			t.Errorf("lens %d name = %q, want %q", i, lens.Name, name)
		}
		if names[i] != name {
			t.Errorf("schema lens %d = %q, want %q", i, names[i], name)
		}
		if strings.TrimSpace(lens.Description) == "" || strings.TrimSpace(lens.DetectionGuidance) == "" {
			t.Errorf("lens %q is missing description or detection guidance", name)
		}
		if !strings.Contains(prompt, "["+name+"]") {
			t.Errorf("reviewer prompt does not name lens %q", name)
		}
	}

	if catalog[1].MechanicalCheck != "test-count-and-tolerance" {
		t.Fatalf("test-capitulation mechanical check = %q, want test-count-and-tolerance", catalog[1].MechanicalCheck)
	}
}

func TestReviewerPromptPrioritizesHistoryLensesWithoutDroppingCatalog(t *testing.T) {
	t.Parallel()

	prompt := lenses.ReviewerPromptWithPriority([]string{"fail-open-default", "test-capitulation"})
	failOpen := strings.Index(prompt, "[fail-open-default]")
	testCapitulation := strings.Index(prompt, "[test-capitulation]")
	vacuous := strings.Index(prompt, "[vacuous-check]")
	if failOpen < 0 || testCapitulation < 0 || vacuous < 0 {
		t.Fatalf("prioritized prompt dropped a lens:\n%s", prompt)
	}
	if !(failOpen < testCapitulation && testCapitulation < vacuous) {
		t.Fatalf("prioritized lens order is wrong:\n%s", prompt)
	}
	for _, name := range lenses.Names() {
		if strings.Count(prompt, "["+name+"]") != 1 {
			t.Fatalf("lens %q not rendered exactly once:\n%s", name, prompt)
		}
	}
}

func TestWorstMissedLensGuidanceNamesCorpusDecisionRules(t *testing.T) {
	t.Parallel()

	want := map[string][]string{
		"vacuous-check":                         {"post-mutation", "same expression"},
		"test-capitulation":                     {"numeric tolerance", "larger threshold"},
		"self-consistent-oracle":                {"literal", "production helper"},
		"comment-defended-workaround":           {"permissive return", "security bypass"},
		"scope-expansion":                       {"new file", "intent"},
		"asserted-followup-without-artifact":    {"issue number", "approval reference"},
		"fail-open-default":                     {"nil, nil", "privileged"},
		"rule-applied-in-one-place-not-sibling": {"versioned", "transport"},
		"redundant-comment":                     {"repeated phrases", "function name"},
	}
	for _, lens := range lenses.Catalog() {
		guidance := strings.ToLower(lens.DetectionGuidance)
		for _, fragment := range want[lens.Name] {
			if !strings.Contains(guidance, fragment) {
				t.Errorf("lens %q guidance is missing %q: %s", lens.Name, fragment, lens.DetectionGuidance)
			}
		}
		if strings.TrimSpace(lens.MechanicalCheck) == "" {
			t.Errorf("lens %q does not name its mechanical pre-check", lens.Name)
		}
	}
}
