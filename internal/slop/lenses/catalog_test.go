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

	if catalog[1].MechanicalCheck != "test-count-floor" {
		t.Fatalf("test-capitulation mechanical check = %q, want test-count-floor", catalog[1].MechanicalCheck)
	}
}
