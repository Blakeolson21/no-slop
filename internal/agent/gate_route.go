package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// gateRouteKey resolves the declaration for this invocation before consulting
// persisted quota state. The gate runner can serve several seats through one
// agent executable, so its executable name is not the quota identity.
func gateRouteKey(opts RunOpts) string {
	if opts.invocationIdentity == nil || opts.invocationIdentity.Executable == nil ||
		filepath.Base(*opts.invocationIdentity.Executable) != "glm-gate-review" {
		return ""
	}
	turn := ""
	for _, entry := range opts.Env {
		if value, ok := strings.CutPrefix(entry, GateTurnKindEnvVar+"="); ok {
			turn = value
		}
	}
	if turn == "" {
		return ""
	}
	path := os.Getenv("MO_GATE_RUNNER_POLICY")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		path = filepath.Join(home, ".config", "mo", "gate-runner-policy.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var policy struct {
		Turns map[string]struct {
			Route           string   `json:"route"`
			Model           string   `json:"model"`
			ClaudeConfigDir string   `json:"claude_config_dir"`
			StatePaths      []string `json:"state_paths"`
		} `json:"turn_declarations"`
	}
	if json.Unmarshal(data, &policy) != nil {
		return ""
	}
	decl, ok := policy.Turns[turn]
	if !ok || decl.Route == "" || decl.Model == "" {
		return ""
	}
	seat := decl.ClaudeConfigDir
	for _, item := range decl.StatePaths {
		if value, ok := strings.CutPrefix(item, "codex_home:"); ok {
			seat = value
		}
	}
	return routeKey(decl.Route, decl.Model, seat)
}

func routeKey(route, model, seat string) string {
	if route == "" || model == "" {
		return ""
	}
	return "route=" + route + " model=" + model + " seat=" + seat
}

// runnerRefusalIdentity reads only the runner's bounded structured diagnostic.
// An arbitrary quota banner has no authority to change a declared route.
func runnerRefusalIdentity(message string) (route, model string) {
	const prefix = "GATE_RUNNER_REFUSAL "
	start := strings.Index(message, prefix)
	if start < 0 {
		return "", ""
	}
	var refusal struct {
		Route string `json:"route"`
		Model string `json:"declared_model"`
	}
	if json.NewDecoder(strings.NewReader(message[start+len(prefix):])).Decode(&refusal) != nil {
		return "", ""
	}
	return refusal.Route, refusal.Model
}
