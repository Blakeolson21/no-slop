package evidence

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/git"
)

const (
	// GateEvidenceRefPrefix is the private namespace in a gate mirror for
	// commits reviewed by a completed review round.
	GateEvidenceRefPrefix = "refs/gate-evidence/"
	zeroObjectID          = "0000000000000000000000000000000000000000"
)

// GateEvidencePinRequest identifies one review tree to retain in the gate
// mirror. GateDir must be the repository's managed bare gate, never the user's
// working repository.
type GateEvidencePinRequest struct {
	GateDir         string
	RunID           string
	Round           int
	ReviewedHeadSHA *string
}

// GateEvidencePinResult reports whether a reviewed head was retained and the
// exact ref used for it. Pinned is false when the round had no reviewed head.
type GateEvidencePinResult struct {
	Ref    string
	SHA    string
	Pinned bool
}

// ConfigureGateGCProtection makes reflogs in the reviewed-tree namespace
// permanent. The refs themselves keep their target commits reachable; the
// namespace-specific reflog policy also protects a reviewed commit if an
// operator temporarily removes a ref before an intentional retention decision.
func ConfigureGateGCProtection(ctx context.Context, gateDir string) error {
	if strings.TrimSpace(gateDir) == "" {
		return fmt.Errorf("gate mirror is empty")
	}
	for _, key := range []string{
		"gc.refs/gate-evidence/*.reflogExpire",
		"gc.refs/gate-evidence/*.reflogExpireUnreachable",
	} {
		if _, err := git.RunBare(ctx, gateDir, "config", key, "never"); err != nil {
			return fmt.Errorf("configure %s: %w", key, err)
		}
	}
	return nil
}

// EnsureGateGCProtection installs the reviewed-tree retention policy only when
// it is absent or has drifted. This keeps already-current gates cheap and
// avoids rewriting their config on every daemon restart.
func EnsureGateGCProtection(ctx context.Context, gateDir string) error {
	if strings.TrimSpace(gateDir) == "" {
		return fmt.Errorf("gate mirror is empty")
	}
	for _, key := range []string{
		"gc.refs/gate-evidence/*.reflogExpire",
		"gc.refs/gate-evidence/*.reflogExpireUnreachable",
	} {
		value, err := git.RunBare(ctx, gateDir, "config", "--get", key)
		if err != nil || strings.TrimSpace(value) != "never" {
			return ConfigureGateGCProtection(ctx, gateDir)
		}
	}
	return nil
}

// PinReviewedTree retains the reviewed commit under one deterministic ref in
// the gate mirror. A nil or empty ReviewedHeadSHA is a valid no-op: the round
// has no evidence to pin. An existing ref for the same round is idempotent
// when it already names the same commit; changing an existing ref is rejected
// so a run cannot silently change which tree its evidence claims to review.
func PinReviewedTree(ctx context.Context, req GateEvidencePinRequest) (*GateEvidencePinResult, error) {
	result := &GateEvidencePinResult{}
	if req.ReviewedHeadSHA == nil || strings.TrimSpace(*req.ReviewedHeadSHA) == "" {
		return result, nil
	}
	gateDir := strings.TrimSpace(req.GateDir)
	if gateDir == "" {
		return nil, fmt.Errorf("cannot pin reviewed tree: gate mirror is empty")
	}
	ref, err := gateEvidenceRef(ctx, gateDir, req.RunID, req.Round)
	if err != nil {
		return nil, err
	}
	sha := strings.TrimSpace(*req.ReviewedHeadSHA)
	resolved, err := git.RunBare(ctx, gateDir, "rev-parse", "--verify", "--end-of-options", sha+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("resolve reviewed head %q in gate mirror: %w", sha, err)
	}

	existing, err := git.RunBare(ctx, gateDir, "for-each-ref", "--format=%(objectname)", ref)
	if err != nil {
		return nil, fmt.Errorf("inspect gate evidence ref %s: %w", ref, err)
	}
	if existing != "" {
		if existing != resolved {
			return nil, fmt.Errorf("gate evidence ref %s already points to %s, cannot repoint it to %s", ref, existing, resolved)
		}
		return &GateEvidencePinResult{Ref: ref, SHA: resolved, Pinned: true}, nil
	}

	if _, err := git.RunBare(ctx, gateDir, "update-ref", "--create-reflog", ref, resolved, zeroObjectID); err != nil {
		// A concurrent writer may have installed the same ref between the
		// inspection and the compare-and-swap. Re-read it and accept only the
		// exact same target.
		current, readErr := git.RunBare(ctx, gateDir, "for-each-ref", "--format=%(objectname)", ref)
		if readErr == nil && current == resolved {
			return &GateEvidencePinResult{Ref: ref, SHA: resolved, Pinned: true}, nil
		}
		return nil, fmt.Errorf("create gate evidence ref %s: %w", ref, err)
	}
	return &GateEvidencePinResult{Ref: ref, SHA: resolved, Pinned: true}, nil
}

func gateEvidenceRef(ctx context.Context, gateDir, runID string, round int) (string, error) {
	if strings.TrimSpace(runID) != runID || runID == "" || strings.HasPrefix(runID, "-") || strings.Contains(runID, "/") {
		return "", fmt.Errorf("invalid gate evidence run id %q: must be one ref path component", runID)
	}
	if round <= 0 {
		return "", fmt.Errorf("invalid gate evidence round %d: must be positive", round)
	}
	ref := GateEvidenceRefPrefix + runID + "/" + strconv.Itoa(round)
	if _, err := git.RunBare(ctx, gateDir, "check-ref-format", ref); err != nil {
		return "", fmt.Errorf("invalid gate evidence ref %q: %w", ref, err)
	}
	return ref, nil
}
