package gatecontext_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/gatecontext"
	"github.com/Blakeolson21/no-slop/internal/paths"
)

func TestInspectorHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (gatecontext.Inspector{Paths: paths.WithRoot(t.TempDir())}).Inspect(ctx, gatecontext.Request{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("classification error = %v, want context.Canceled", err)
	}
}
