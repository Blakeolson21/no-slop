package db

import (
	"encoding/json"
	"github.com/Blakeolson21/no-slop/internal/types"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGateWasteTerminalRowIncludesPartialAndUnknownInvocations(t *testing.T) {
	for _, status := range []types.RunStatus{types.RunCancelled, types.RunFailed} {
		t.Run(string(status), func(t *testing.T) {
			d := openTestDB(t)
			repo, _ := d.InsertRepo("/test", "https://example.com/test", "main")
			r, _ := d.InsertRun(repo.ID, "lane", "head", "base")
			input, output, cache := 123, 45, 0
			provider, model := "openai", "unknown-model"
			inv := AgentInvocation{RunID: r.ID, StepName: "review", Purpose: "review", Agent: "codex", ModelProvider: &provider, Model: &model, DeltaInputTokens: &input, DeltaOutputTokens: &output, DeltaCacheReadTokens: &cache, SessionMode: "cold", ExitStatus: "error"}
			if _, err := d.InsertAgentInvocation(inv); err != nil {
				t.Fatal(err)
			}
			inv.Purpose = "review-fix"
			inv.Model = nil
			inv.DeltaInputTokens = nil

			inv.DeltaOutputTokens = nil
			if _, err := d.InsertAgentInvocation(inv); err != nil {
				t.Fatal(err)
			}
			if err := d.UpdateRunErrorStatus(r.ID, "partial failure", status); err != nil {
				t.Fatal(err)
			}
			var raw string
			if err := d.sql.QueryRow("SELECT waste_usage_json FROM runs WHERE id=?", r.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var row struct {
				Schema int              `json:"schema"`
				Turns  []map[string]any `json:"turns"`
			}
			if err := json.Unmarshal([]byte(raw), &row); err != nil {
				t.Fatal(err)
			}
			if row.Schema != 1 || len(row.Turns) != 2 {
				t.Fatalf("bad terminal usage: %s", raw)
			}
			if row.Turns[0]["input_tokens"] != float64(123) || row.Turns[1]["input_tokens"] != nil || row.Turns[1]["estimated_cost_usd"] != nil {
				t.Fatalf("unknown/partial usage lost: %s", raw)
			}
			if err := d.UpdateRunErrorStatus(r.ID, "diagnostic", status); err != nil {
				t.Fatal(err)
			}
			var again string
			d.sql.QueryRow("SELECT waste_usage_json FROM runs WHERE id=?", r.ID).Scan(&again)
			if raw != again {
				t.Fatalf("terminal refresh double counted or repriced usage")
			}
		})
	}
}

func dropGateWasteTriggersForLegacyFixture(t *testing.T, d *DB) {
	t.Helper()
	for _, name := range []string{"gate_waste_terminal", "gate_waste_late_attempt", "gate_waste_completed_attempt"} {
		if _, err := d.sql.Exec("DROP TRIGGER IF EXISTS " + name); err != nil {
			t.Fatal(err)
		}
	}
}

// authoritativeLedger writes a model ledger the Quartermaster owner accepts:
// version 1, generated_at, and rows carrying only the closed MODEL_ROW_KEYS
// field set. Fixtures must use a document both owners agree is valid, or the
// test proves pricing against a file production would refuse.
func authoritativeLedger(t *testing.T, rows string) {
	t.Helper()
	ledger := filepath.Join(t.TempDir(), "model-ledger.json")
	doc := `{"version":1,"generated_at":1757000000,"models":[` + rows + `]}`
	if err := os.WriteFile(ledger, []byte(doc), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QM_MODEL_LEDGER", ledger)
}

const standardLedgerRow = `{"key":"model","display_name":"Model","model_id":"provider/model","provider":"provider",` +
	`"context_window":200000,"max_output_tokens":32000,"cost_class":"standard","input_per_mtok":2,"output_per_mtok":8,` +
	`"strengths":["review"],"verified_at":"2026-09-01","evidence_url":"https://example.com/pricing"}`

func TestGateWastePriceTableAndLateMeasuredUpdate(t *testing.T) {
	authoritativeLedger(t, standardLedgerRow)
	d := openTestDB(t)
	repo, _ := d.InsertRepo("/test", "https://example.com/test", "main")
	r, _ := d.InsertRun(repo.ID, "lane", "head", "base")
	pending, err := d.InsertAgentInvocation(AgentInvocation{RunID: r.ID, StepName: "review", Purpose: "review", SessionMode: "cold", ExitStatus: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err = d.UpdateRunErrorStatus(r.ID, "cancelled during review", types.RunCancelled); err != nil {
		t.Fatal(err)
	}
	provider, model := "provider", "provider/model"
	input, output, cache, write := 1000, 100, 200, 20
	inv := AgentInvocation{RunID: r.ID, StepName: "review", Purpose: "review", SessionMode: "cold", ModelProvider: &provider, Model: &model, DeltaInputTokens: &input, DeltaOutputTokens: &output, DeltaCacheReadTokens: &cache, CacheCreationTokens: &write, ExitStatus: "cancelled"}
	// The authoritative ledger carries no cache rates, so measured cache usage
	// leaves the estimate unknown while the priced subtotal stays exact.
	measured, err := d.CompleteAgentInvocation(inv, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantKnown := float64(800*2+100*8) / 1e6
	if measured.EstimatedCostUSD != nil {
		t.Fatalf("uncharged cache usage reported as a complete estimate: %v", *measured.EstimatedCostUSD)
	}
	if measured.KnownCostUSD == nil || math.Abs(*measured.KnownCostUSD-wantKnown) > 1e-12 {
		t.Fatalf("known=%v want=%v", measured.KnownCostUSD, wantKnown)
	}
	noCache, noWrite := 0, 0
	complete := inv
	complete.DeltaCacheReadTokens, complete.CacheCreationTokens = &noCache, &noWrite
	priced, err := d.CompleteAgentInvocation(complete, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := float64(1000*2+100*8) / 1e6
	if priced.EstimatedCostUSD == nil || math.Abs(*priced.EstimatedCostUSD-want) > 1e-12 {
		t.Fatalf("price=%v want=%v", priced.EstimatedCostUSD, want)
	}
	var raw string
	if err = d.sql.QueryRow("SELECT waste_usage_json FROM runs WHERE id=?", r.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Turns []struct {
			ID   string   `json:"turn_id"`
			Cost *float64 `json:"estimated_cost_usd"`
		} `json:"turns"`
	}
	if err = json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Turns) != 1 || payload.Turns[0].ID != pending.ID || payload.Turns[0].Cost == nil {
		t.Fatalf("late usage duplicated/lost: %s", raw)
	}
	inv.SessionMode = InvocationModeResumed
	if _, err = d.CompleteAgentInvocation(inv, pending.ID); err != nil {
		t.Fatal(err)
	}
	var cacheJSON string
	if err = d.sql.QueryRow("SELECT json_extract(waste_usage_json,'$.turns[0]') FROM runs WHERE id=?", r.ID).Scan(&cacheJSON); err != nil {
		t.Fatal(err)
	}
	var cacheRow map[string]any
	if err = json.Unmarshal([]byte(cacheJSON), &cacheRow); err != nil {
		t.Fatal(err)
	}
	if cacheRow["cache_creation_tokens"] != nil || cacheRow["reported_cache_creation_tokens"] != float64(write) {
		t.Fatalf("cumulative cache tokens counted twice: %s", cacheJSON)
	}
	inv.SessionMode = InvocationModeCold
	inv.DeltaOutputTokens = nil
	partial, err := d.CompleteAgentInvocation(inv, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if partial.EstimatedCostUSD != nil || partial.KnownCostUSD == nil {
		t.Fatalf("partial usage falsely complete: %+v", partial)
	}
	inv.ModelProvider = nil
	unknown, err := d.CompleteAgentInvocation(inv, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unknown.EstimatedCostUSD != nil || unknown.KnownCostUSD != nil {
		t.Fatal("unknown provider priced")
	}
}

// A table shape the Quartermaster owner refuses must not produce a confident
// price here. The cache-rate fields below are outside its closed MODEL_ROW_KEYS,
// so the whole document is non-authoritative.
func TestGatePricingRefusesTablesQuartermasterRejects(t *testing.T) {
	provider, model := "provider", "provider/model"
	input, output := 1000, 100
	for name, rows := range map[string]string{
		"cache rate fields": `{"key":"model","display_name":"Model","model_id":"provider/model","provider":"provider",` +
			`"context_window":200000,"max_output_tokens":32000,"cost_class":"standard","input_per_mtok":2,"output_per_mtok":8,` +
			`"cache_read_per_mtok":0.2,"cache_write_per_mtok":2.5,` +
			`"strengths":["review"],"verified_at":"2026-09-01","evidence_url":"https://example.com/pricing"}`,
		"missing metadata":   `{"key":"model","model_id":"provider/model","provider":"provider","cost_class":"standard","input_per_mtok":2,"output_per_mtok":8}`,
		"free row priced":    `{"key":"model","display_name":"Model","model_id":"provider/model","provider":"provider","context_window":200000,"max_output_tokens":32000,"cost_class":"free","input_per_mtok":2,"output_per_mtok":8,"strengths":["review"],"verified_at":"2026-09-01","evidence_url":"https://example.com/pricing"}`,
		"unknown cost class": `{"key":"model","display_name":"Model","model_id":"provider/model","provider":"provider","context_window":200000,"max_output_tokens":32000,"cost_class":"cheap","input_per_mtok":2,"output_per_mtok":8,"strengths":["review"],"verified_at":"2026-09-01","evidence_url":"https://example.com/pricing"}`,
	} {
		t.Run(name, func(t *testing.T) {
			authoritativeLedger(t, rows)
			inv := AgentInvocation{ModelProvider: &provider, Model: &model, DeltaInputTokens: &input, DeltaOutputTokens: &output, SessionMode: "cold"}
			priceGateInvocation(&inv)
			if inv.EstimatedCostUSD != nil || inv.KnownCostUSD != nil || inv.CostBasis != nil {
				t.Fatalf("non-authoritative ledger priced: %v %v", inv.EstimatedCostUSD, inv.CostBasis)
			}
			if inv.PriceSourceJSON == nil || !strings.Contains(*inv.PriceSourceJSON, `"status":"invalid"`) {
				t.Fatalf("refusal not recorded: %v", inv.PriceSourceJSON)
			}
		})
	}
}

// A validated free/local row bills nothing for any token class, so its exact
// cost is zero even when cache usage is measured - not unknown.
func TestGatePricingFreeClassIsAnExactZero(t *testing.T) {
	authoritativeLedger(t, `{"key":"local-model","display_name":"Local","model_id":"local/model","provider":"local",`+
		`"context_window":128000,"max_output_tokens":8192,"cost_class":"free","input_per_mtok":0,"output_per_mtok":0,`+
		`"strengths":["local"],"verified_at":"2026-09-01","evidence_url":"https://example.com/local"}`)
	provider, model := "local", "local/model"
	input, cache := 100, 20
	inv := AgentInvocation{ModelProvider: &provider, Model: &model, DeltaInputTokens: &input, DeltaCacheReadTokens: &cache, SessionMode: InvocationModeResumed}
	priceGateInvocation(&inv)
	if inv.CostBasis == nil || *inv.CostBasis != "free" {
		t.Fatalf("basis=%v", inv.CostBasis)
	}
	if inv.EstimatedCostUSD == nil || *inv.EstimatedCostUSD != 0 || inv.KnownCostUSD == nil || *inv.KnownCostUSD != 0 {
		t.Fatalf("free usage not an exact zero: estimated=%v known=%v", inv.EstimatedCostUSD, inv.KnownCostUSD)
	}
}
