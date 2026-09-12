package db

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Price only observed model/provider identities and measured per-attempt deltas.
// The Quartermaster table is the existing MO price authority. No network calls,
// neighboring model fallback, or hard-coded price is permitted here. Cache rates
// absent from the table remain unknown whenever nonzero cache usage needs them.
func priceGateInvocation(inv *AgentInvocation) {
	inv.EstimatedCostUSD = nil
	inv.KnownCostUSD = nil
	inv.CostBasis = nil
	inv.PriceSourceJSON = nil
	home, _ := os.UserHomeDir()
	path := os.Getenv("MO_MODEL_LEDGER")
	if path == "" {
		path = filepath.Join(home, ".config", "mo", "quartermaster", "model-ledger.json")
	}
	source := map[string]any{"path": path, "sha256": nil, "status": "unavailable"}
	defer func() { raw, _ := json.Marshal(source); s := string(raw); inv.PriceSourceJSON = &s }()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	hash := sha256.Sum256(data)
	source["sha256"] = hex.EncodeToString(hash[:])
	source["status"] = "unmatched"
	var table struct {
		Models []struct {
			Key       string   `json:"key"`
			ModelID   string   `json:"model_id"`
			Provider  string   `json:"provider"`
			CostClass string   `json:"cost_class"`
			Input     *float64 `json:"input_per_mtok"`
			Output    *float64 `json:"output_per_mtok"`
			Read      *float64 `json:"cache_read_per_mtok"`
			Write     *float64 `json:"cache_write_per_mtok"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &table) != nil || table.Models == nil {
		source["status"] = "invalid"
		return
	}
	if inv.Model == nil || inv.ModelProvider == nil {
		return
	}
	match := -1
	for i, r := range table.Models {
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
	row := table.Models[match]
	valid := func(x *float64) bool { return x != nil && !math.IsNaN(*x) && !math.IsInf(*x, 0) && *x >= 0 }
	if !valid(row.Input) || !valid(row.Output) {
		source["status"] = "invalid"
		return
	}
	basis := "list_imputed" // This is an estimate, never a metered payment claim.
	switch row.CostClass {
	case "free", "local":
		if *row.Input != 0 || *row.Output != 0 {
			source["status"] = "invalid"
			return
		}
		basis = "free"
	case "economy", "standard", "premium", "frontier":
		if *row.Input <= 0 || *row.Output <= 0 {
			source["status"] = "invalid"
			return
		}
	default:
		source["status"] = "invalid"
		return
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
		if !valid(rate) {
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
	charge(inv.DeltaCacheReadTokens, row.Read)
	// Cache-creation counters have no per-session delta contract in legacy rows.
	// On resumed sessions an observed raw cumulative value cannot be charged twice.
	if inv.SessionMode == InvocationModeResumed {
		complete = false
	} else {
		charge(inv.CacheCreationTokens, row.Write)
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
