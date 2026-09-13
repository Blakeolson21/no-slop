package pipeline

import (
	"testing"

	"github.com/Blakeolson21/no-slop/internal/config"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestAutoFixLimitForStepConfiguredZeroDisablesRecordedPositiveLimit(t *testing.T) {
	recorded := 3
	e := &Executor{config: &config.Config{AutoFix: config.AutoFix{Review: 0}}}
	if got := e.autoFixLimitForStep(types.StepReview, &recorded); got != 0 {
		t.Fatalf("effective limit = %d, want 0", got)
	}
}

func TestAutoFixLimitForStepWithoutConfigKeepsRecordedPositiveLimit(t *testing.T) {
	recorded := 3
	e := &Executor{}
	if got := e.autoFixLimitForStep(types.StepReview, &recorded); got != recorded {
		t.Fatalf("effective limit = %d, want %d", got, recorded)
	}
}
