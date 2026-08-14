package daemon

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/Blakeolson21/no-slop/internal/winproc"
)

func serviceDefinitionMatchesRoot(data []byte, p *paths.Paths) bool {
	if len(data) == 0 {
		return false
	}
	if p == nil {
		return true
	}
	want := canonicalRoot(p.Root())
	for _, root := range serviceDefinitionRoots(data) {
		if canonicalRoot(root) == want {
			return true
		}
	}
	return false
}

func serviceDefinitionRoots(data []byte) []string {
	var roots []string
	text := string(data)
	if root, ok := launchAgentRoot(data); ok {
		roots = append(roots, root)
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "WorkingDirectory=") {
			continue
		}
		if root, ok := serviceDefinitionArg(strings.TrimSpace(strings.TrimPrefix(line, "WorkingDirectory="))); ok {
			roots = append(roots, root)
		}
	}
	if root, ok := windowsTaskRoot(data); ok {
		roots = append(roots, root)
	}
	return roots
}

func launchAgentRoot(data []byte) (string, bool) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var sawProgramArguments bool
	var inProgramArguments bool
	var args []string
	for {
		token, err := decoder.Token()
		if err != nil {
			return rootArg(args)
		}
		switch t := token.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "key":
				var key string
				if err := decoder.DecodeElement(&key, &t); err != nil {
					return "", false
				}
				sawProgramArguments = strings.TrimSpace(key) == "ProgramArguments"
			case "array":
				if sawProgramArguments {
					inProgramArguments = true
					sawProgramArguments = false
				}
			case "string":
				if !inProgramArguments {
					sawProgramArguments = false
					continue
				}
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return "", false
				}
				args = append(args, value)
			default:
				if !inProgramArguments {
					sawProgramArguments = false
				}
			}
		case xml.EndElement:
			if inProgramArguments && t.Name.Local == "array" {
				return rootArg(args)
			}
		}
	}
}

func windowsTaskRoot(data []byte) (string, bool) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", false
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Arguments" {
			continue
		}
		var value string
		if err := decoder.DecodeElement(&value, &start); err != nil {
			return "", false
		}
		if root, ok := rootArg(commandFields(value)); ok {
			return root, true
		}
	}
}

func rootArg(args []string) (string, bool) {
	for i, arg := range args {
		if arg == "--root" && i+1 < len(args) {
			return args[i+1], strings.TrimSpace(args[i+1]) != ""
		}
		if value, ok := strings.CutPrefix(arg, "--root="); ok {
			return value, strings.TrimSpace(value) != ""
		}
	}
	return "", false
}

func serviceDefinitionArg(value string) (string, bool) {
	if value == "" {
		return "", false
	}
	if value[0] == '"' {
		unquoted, err := strconv.Unquote(value)
		if err != nil || strings.TrimSpace(unquoted) == "" {
			return "", false
		}
		return unquoted, true
	}
	return value, strings.TrimSpace(value) != ""
}

func commandFields(command string) []string {
	var fields []string
	for i := 0; i < len(command); {
		for i < len(command) && (command[i] == ' ' || command[i] == '\t') {
			i++
		}
		if i >= len(command) {
			break
		}
		if command[i] != '"' {
			start := i
			for i < len(command) && command[i] != ' ' && command[i] != '\t' {
				i++
			}
			fields = append(fields, command[start:i])
			continue
		}
		start := i
		i++
		escaped := false
		for i < len(command) {
			c := command[i]
			i++
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				break
			}
		}
		value, err := strconv.Unquote(command[start:i])
		if err != nil {
			continue
		}
		fields = append(fields, value)
	}
	return fields
}

func xmlEscaped(value string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(value))
	return buf.String()
}

func launchAgentExecutable(data []byte) (string, bool) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var sawProgramArguments bool
	var inProgramArguments bool
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", false
		}
		switch t := token.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "key":
				var key string
				if err := decoder.DecodeElement(&key, &t); err != nil {
					return "", false
				}
				sawProgramArguments = strings.TrimSpace(key) == "ProgramArguments"
			case "array":
				if sawProgramArguments {
					inProgramArguments = true
					sawProgramArguments = false
				}
			case "string":
				if !inProgramArguments {
					sawProgramArguments = false
					continue
				}
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return "", false
				}
				if strings.TrimSpace(value) == "" {
					return "", false
				}
				return value, true
			default:
				if !inProgramArguments {
					sawProgramArguments = false
				}
			}
		case xml.EndElement:
			if inProgramArguments && t.Name.Local == "array" {
				return "", false
			}
		}
	}
}

func systemdUnitExecutable(data []byte) (string, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		return firstCommandArg(strings.TrimSpace(strings.TrimPrefix(line, "ExecStart=")))
	}
	return "", false
}

// isProxyEnvKey reports whether key is one of the forwarded proxy variables in
// proxyEnvKeys. The match is exact on case-sensitive platforms (macOS, Linux)
// so HTTP_PROXY and http_proxy stay distinct; on Windows, where env-var names
// are case-insensitive, it matches case-insensitively to mirror serviceProxyEnv.
func isProxyEnvKey(key string) bool {
	for _, k := range proxyEnvKeys {
		if k == key || (runtimeGOOS == "windows" && strings.EqualFold(k, key)) {
			return true
		}
	}
	return false
}

// systemdUnitProxyEnv extracts the forwarded proxy `Environment=` entries from a
// rendered systemd unit, mirroring systemdUnitExecutable. Entries are returned
// in file order with systemd's `%` -> `%%` doubling undone, so feeding the
// result back through renderSystemdUnitWithProxyEnv reproduces the unit
// byte-for-byte. The fixed HOME/PATH Environment= lines are skipped. This lets
// drift detection (and a reinstall) inherit a proxy already baked into the
// on-disk unit when the current shell has no proxy variables exported, instead
// of silently stripping it. See serviceProxyEnv.
func systemdUnitProxyEnv(data []byte) [][2]string {
	var out [][2]string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Environment=") {
			continue
		}
		assignment, err := strconv.Unquote(strings.TrimPrefix(line, "Environment="))
		if err != nil {
			continue
		}
		// Undo the %% -> % doubling systemdEnvironmentLine applied; keys never
		// contain %, so undoing before the split is safe.
		assignment = strings.ReplaceAll(assignment, "%%", "%")
		key, value, ok := strings.Cut(assignment, "=")
		if !ok || !isProxyEnvKey(key) {
			continue
		}
		out = append(out, [2]string{key, value})
	}
	return out
}

// launchAgentProxyEnv extracts the forwarded proxy entries from the
// EnvironmentVariables <dict> of a rendered launchd plist, mirroring
// launchAgentExecutable. Pairs are returned in file order with XML escaping
// undone by the decoder; the fixed HOME/PATH keys are skipped. Feeding the
// result back through renderLaunchAgentWithProxyEnv reproduces the plist. See
// serviceProxyEnv.
func launchAgentProxyEnv(data []byte) [][2]string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var out [][2]string
	var sawEnvVarsKey bool // the most recent <key> was "EnvironmentVariables"
	var inEnvDict bool     // currently inside the EnvironmentVariables <dict>
	var pendingKey string
	var havePendingKey bool
	for {
		token, err := decoder.Token()
		if err != nil {
			return out
		}
		switch t := token.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "key":
				var key string
				if err := decoder.DecodeElement(&key, &t); err != nil {
					return out
				}
				key = strings.TrimSpace(key)
				if inEnvDict {
					pendingKey = key
					havePendingKey = true
				} else {
					sawEnvVarsKey = key == "EnvironmentVariables"
				}
			case "dict":
				if sawEnvVarsKey {
					inEnvDict = true
					sawEnvVarsKey = false
				}
			case "string":
				if !inEnvDict || !havePendingKey {
					continue
				}
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return out
				}
				if isProxyEnvKey(pendingKey) {
					out = append(out, [2]string{pendingKey, value})
				}
				havePendingKey = false
			}
		case xml.EndElement:
			if inEnvDict && t.Name.Local == "dict" {
				return out
			}
		}
	}
}

func firstCommandArg(command string) (string, bool) {
	if command == "" {
		return "", false
	}
	if command[0] != '"' {
		fields := strings.Fields(command)
		if len(fields) == 0 || fields[0] == "" {
			return "", false
		}
		return fields[0], true
	}
	escaped := false
	for i := 1; i < len(command); i++ {
		c := command[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			value, err := strconv.Unquote(command[:i+1])
			if err != nil || value == "" {
				return "", false
			}
			return value, true
		}
	}
	return "", false
}

func runServiceCommand(name string, args ...string) ([]byte, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, args...)
	winproc.Harden(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s %s: %w: %s", path, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}
