package testfloor_test

import (
	"testing"

	"github.com/Blakeolson21/no-slop/internal/slop/testfloor"
)

func TestCompareFailsWhenCurrentTestCountDropsBelowBaseline(t *testing.T) {
	t.Parallel()

	baseline := []testfloor.File{{
		Path: "widget_test.go",
		Content: `package widget
func TestCreatesWidget(t *testing.T) {}
func TestRejectsEmptyName(t *testing.T) {}
`,
	}}
	current := []testfloor.File{{
		Path: "widget_test.go",
		Content: `package widget
func TestCreatesWidget(t *testing.T) {}
`,
	}}

	result := testfloor.Compare(baseline, current)
	if result.Passed {
		t.Fatalf("result = %+v, want failed floor", result)
	}
	if result.Baseline != 2 || result.Current != 1 || result.Delta != -1 {
		t.Fatalf("result = %+v, want baseline 2, current 1, delta -1", result)
	}
}

func TestCountRecognizesCommonTestFrameworkDeclarations(t *testing.T) {
	t.Parallel()

	files := []testfloor.File{
		{Path: "test_widget.py", Content: "def test_widget():\n    pass\nclass TestWidget:\n    def test_name(self):\n        pass\n"},
		{Path: "widget.test.ts", Content: "describe('widget', () => { it('works', () => {}); test('fails closed', () => {}); });"},
		{Path: "widget_spec.rb", Content: "it 'works' do\nend\nspecify 'fails closed' do\nend\n"},
		{Path: "WidgetTest.java", Content: "@Test\nvoid createsWidget() {}\n@Test\nvoid rejectsEmptyName() {}\n"},
		{Path: "widget.rs", Content: "#[test]\nfn creates_widget() {}\n#[test]\nfn rejects_empty_name() {}\n"},
	}
	if got := testfloor.Count(files); got != 10 {
		t.Fatalf("count = %d, want 10", got)
	}
}
