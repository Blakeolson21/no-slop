package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemoTapeForcesDetachedDaemon(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "demo.tape"))
	if err != nil {
		t.Fatalf("read demo.tape: %v", err)
	}
	env := parseTapeEnvCommands(t, string(data))
	if env["NS_TEST_START_DAEMON"] != "1" {
		t.Fatal(`demo.tape must force detached daemon startup for demo mode`)
	}
	if env["NS_NO_UPDATE_CHECK"] != "1" {
		t.Fatal(`demo.tape must use the canonical update-check opt-out env`)
	}
	if _, ok := env["NO_MISTAKES_NO_UPDATE_CHECK"]; ok {
		t.Fatal(`demo.tape must not lead with the legacy update-check opt-out env`)
	}
}

func parseTapeEnvCommands(t *testing.T, tape string) map[string]string {
	t.Helper()
	env := map[string]string{}
	for _, line := range splitTapeLines(tape) {
		fields := tapeFields(line)
		if len(fields) == 0 || fields[0] != "Env" {
			continue
		}
		if len(fields) != 3 {
			t.Fatalf("unsupported Env command %q", line)
		}
		env[fields[1]] = fields[2]
	}
	return env
}

func splitTapeLines(tape string) []string {
	var lines []string
	for _, line := range strings.Split(tape, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func tapeFields(line string) []string {
	var fields []string
	for i := 0; i < len(line); {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= len(line) {
			break
		}
		if line[i] == '"' {
			i++
			start := i
			for i < len(line) && line[i] != '"' {
				i++
			}
			fields = append(fields, line[start:i])
			if i < len(line) {
				i++
			}
			continue
		}
		start := i
		for i < len(line) && line[i] != ' ' && line[i] != '\t' {
			i++
		}
		fields = append(fields, line[start:i])
	}
	return fields
}
