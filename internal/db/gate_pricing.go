package db

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// modelLedgerRow mirrors the Quartermaster model-ledger row exactly. The field
// set is closed on both sides: tools/fleet/quartermaster.py refuses a row with
// an unknown field, so this decoder must refuse the same documents rather than
// quietly accepting a shape its owner rejects. In particular the ledger carries
// no cache-rate fields at all, which is why cache tokens are never charged.
type modelLedgerRow struct {
	Key             string   `json:"key"`
	DisplayName     string   `json:"display_name"`
	Provider        string   `json:"provider"`
	ModelID         string   `json:"model_id"`
	ContextWindow   *int64   `json:"context_window"`
	MaxOutputTokens *int64   `json:"max_output_tokens"`
	CostClass       string   `json:"cost_class"`
	Input           *float64 `json:"input_per_mtok"`
	Output          *float64 `json:"output_per_mtok"`
	Strengths       []string `json:"strengths"`
	VerifiedAt      string   `json:"verified_at"`
	EvidenceURL     string   `json:"evidence_url"`
	PricingURL      *string  `json:"pricing_url"`
	Note            *string  `json:"note"`
}

type modelLedger struct {
	Version     *int             `json:"version"`
	GeneratedAt *float64         `json:"generated_at"`
	Comment     json.RawMessage  `json:"_comment"`
	Models      []modelLedgerRow `json:"models"`
}

// unpricedCostClasses and pricedCostClasses are the Quartermaster COST_CLASSES
// vocabulary, split the same way UNPRICED_COST_CLASSES splits it.
var unpricedCostClasses = map[string]bool{"free": true, "local": true}
var pricedCostClasses = map[string]bool{"economy": true, "standard": true, "premium": true, "frontier": true}

// modelLedgerPath resolves the same file the ledger's existing readers do, and
// honors both established overrides: MO_MODEL_LEDGER, used by MO's own
// tools/mo/gateway_pricing.py, and QM_MODEL_LEDGER, used by the schema owner
// tools/fleet/quartermaster.py. Recognizing only one of them would let this
// reader price from a file a sibling owner never saw.
func modelLedgerPath() string {
	for _, key := range []string{"MO_MODEL_LEDGER", "QM_MODEL_LEDGER"} {
		if path := os.Getenv(key); path != "" {
			return path
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "mo", "quartermaster", "model-ledger.json")
}

// loadModelLedger validates the document the way Quartermaster's
// _validate_model_ledger does. Anything it would refuse is refused here, so a
// malformed or non-authoritative file yields no price at all instead of a
// confident number its owner would have rejected.
func loadModelLedger(data []byte) (*modelLedger, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var ledger modelLedger
	if err := decoder.Decode(&ledger); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, fmt.Errorf("model ledger has trailing content")
	}
	if ledger.Version == nil || *ledger.Version != 1 {
		return nil, fmt.Errorf("model ledger must be an object with version 1")
	}
	if ledger.GeneratedAt == nil || math.IsNaN(*ledger.GeneratedAt) || math.IsInf(*ledger.GeneratedAt, 0) {
		return nil, fmt.Errorf("model ledger has an invalid generated_at")
	}
	if len(ledger.Models) == 0 {
		return nil, fmt.Errorf("model ledger must contain at least one model")
	}
	keys := map[string]bool{}
	selectors := map[string]int{}
	for i := range ledger.Models {
		row := &ledger.Models[i]
		if err := validateModelLedgerRow(row); err != nil {
			return nil, err
		}
		if keys[row.Key] {
			return nil, fmt.Errorf("model ledger has a duplicate key: %s", row.Key)
		}
		keys[row.Key] = true
		// key, model_id and display_name are one selectable namespace, so a name
		// two rows both answer to is an ambiguity refused at the file, exactly as
		// Quartermaster refuses it.
		for _, name := range []string{row.Key, row.ModelID, row.DisplayName} {
			if owner, seen := selectors[name]; seen && owner != i {
				return nil, fmt.Errorf("model ledger has an ambiguous selector: %s", name)
			}
			selectors[name] = i
		}
	}
	return &ledger, nil
}

func validateModelLedgerRow(row *modelLedgerRow) error {
	for name, value := range map[string]string{"key": row.Key, "display_name": row.DisplayName, "provider": row.Provider, "model_id": row.ModelID, "verified_at": row.VerifiedAt, "evidence_url": row.EvidenceURL} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("model ledger row has an invalid %s", name)
		}
	}
	if row.ContextWindow == nil || *row.ContextWindow <= 0 || row.MaxOutputTokens == nil || *row.MaxOutputTokens <= 0 {
		return fmt.Errorf("model %s has an invalid context_window or max_output_tokens", row.Key)
	}
	if *row.MaxOutputTokens > *row.ContextWindow {
		return fmt.Errorf("model %s claims max_output_tokens above its context_window", row.Key)
	}
	if len(row.Strengths) == 0 {
		return fmt.Errorf("model %s must list at least one strength", row.Key)
	}
	finite := func(x *float64) bool { return x != nil && !math.IsNaN(*x) && !math.IsInf(*x, 0) && *x >= 0 }
	if !finite(row.Input) || !finite(row.Output) {
		return fmt.Errorf("model %s has an invalid price", row.Key)
	}
	anyPrice := *row.Input > 0 || *row.Output > 0
	allPrices := *row.Input > 0 && *row.Output > 0
	switch {
	case unpricedCostClasses[row.CostClass]:
		if anyPrice {
			return fmt.Errorf("model %s is cost_class %s but carries a nonzero price", row.Key, row.CostClass)
		}
	case pricedCostClasses[row.CostClass]:
		if !allPrices {
			return fmt.Errorf("model %s is cost_class %s but does not carry positive input and output prices", row.Key, row.CostClass)
		}
	default:
		return fmt.Errorf("model %s has an invalid cost_class: %s", row.Key, row.CostClass)
	}
	return nil
}

// Price only observed model/provider identities and measured per-attempt deltas.
// The Quartermaster table is the existing MO price authority. No network calls,
// neighboring model fallback, or hard-coded price is permitted here. The
// authoritative table carries no cache rates, so nonzero cache usage on a priced
// row leaves the estimate unknown rather than understated.
func priceGateInvocation(inv *AgentInvocation) {
	inv.EstimatedCostUSD = nil
	inv.KnownCostUSD = nil
	inv.CostBasis = nil
	inv.PriceSourceJSON = nil
	path := modelLedgerPath()
	source := map[string]any{"path": path, "sha256": nil, "status": "unavailable"}
	defer func() { raw, _ := json.Marshal(source); s := string(raw); inv.PriceSourceJSON = &s }()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	hash := sha256.Sum256(data)
	source["sha256"] = hex.EncodeToString(hash[:])
	source["status"] = "unmatched"
	ledger, err := loadModelLedger(data)
	if err != nil {
		source["status"] = "invalid"
		return
	}
	if inv.Model == nil || inv.ModelProvider == nil {
		return
	}
	match := -1
	for i, r := range ledger.Models {
		if (strings.EqualFold(r.Key, *inv.Model) || strings.EqualFold(r.ModelID, *inv.Model)) && strings.EqualFold(r.Provider, *inv.ModelProvider) {
			if match >= 0 {
				source["status"] = "ambiguous"
				return
			}
			match = i
		}
	}
	if match < 0 {
		return
	}
	row := ledger.Models[match]
	basis := "list_imputed" // This is an estimate, never a metered payment claim.
	if unpricedCostClasses[row.CostClass] {
		basis = "free"
	}
	inv.CostBasis = &basis
	source["status"] = "matched"
	for _, count := range []*int{inv.DeltaInputTokens, inv.DeltaOutputTokens, inv.DeltaCacheReadTokens, inv.CacheCreationTokens} {
		if count != nil && *count < 0 {
			source["status"] = "invalid_usage"
			return
		}
	}
	if inv.DeltaInputTokens != nil && inv.DeltaCacheReadTokens != nil && *inv.DeltaCacheReadTokens > *inv.DeltaInputTokens {
		source["status"] = "invalid_usage"
		return
	}

	if basis == "free" {
		// A validated free/local row bills nothing for any token class, cache
		// tokens included, so the observations leave nothing unknown: the exact
		// cost is zero. Reporting it as incomplete would file a known zero as an
		// unpriced turn.
		zero := 0.0
		inv.KnownCostUSD = &zero
		inv.EstimatedCostUSD = &zero
		return
	}

	known := 0.0
	complete := true
	observations := 0
	charge := func(tokens *int, rate *float64) {
		if tokens == nil || *tokens < 0 {
			complete = false
			return
		}
		if *tokens == 0 {
			observations++
			return
		}
		if rate == nil {
			complete = false
			return
		}
		known += float64(*tokens) * (*rate) / 1e6
		observations++
	}
	var fresh *int
	if inv.DeltaInputTokens != nil && inv.DeltaCacheReadTokens != nil && *inv.DeltaInputTokens >= *inv.DeltaCacheReadTokens && *inv.DeltaCacheReadTokens >= 0 {
		n := *inv.DeltaInputTokens - *inv.DeltaCacheReadTokens
		fresh = &n
	}
	charge(fresh, row.Input)
	charge(inv.DeltaOutputTokens, row.Output)
	// The authoritative ledger has no cache read or cache creation rate, so any
	// nonzero cache usage on a priced row is charged at an unknown rate.
	charge(inv.DeltaCacheReadTokens, nil)
	// Cache-creation counters have no per-session delta contract in legacy rows.
	// On resumed sessions an observed raw cumulative value cannot be charged twice.
	if inv.SessionMode == InvocationModeResumed {
		complete = false
	} else {
		charge(inv.CacheCreationTokens, nil)
	}
	if math.IsInf(known, 0) || math.IsNaN(known) {
		source["status"] = "invalid"
		return
	}
	if observations > 0 {
		inv.KnownCostUSD = &known
	}
	if complete {
		inv.EstimatedCostUSD = &known
	}
}
