package db

import (
	"encoding/json"
	"github.com/Blakeolson21/no-slop/internal/types"
	"math"
	"os"
	"path/filepath"
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

func TestGateWastePriceTableAndLateMeasuredUpdate(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "model-ledger.json")
	if err := os.WriteFile(ledger, []byte(`{"models":[{"key":"model","model_id":"provider/model","provider":"provider","cost_class":"standard","input_per_mtok":2,"output_per_mtok":8,"cache_read_per_mtok":0.2,"cache_write_per_mtok":2.5}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MO_MODEL_LEDGER", ledger)
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
	measured, err := d.CompleteAgentInvocation(inv, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := float64(800*2+100*8+200*.2+20*2.5) / 1e6
	if measured.EstimatedCostUSD == nil || math.Abs(*measured.EstimatedCostUSD-want) > 1e-12 {
		t.Fatalf("price=%v want=%v", measured.EstimatedCostUSD, want)
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
