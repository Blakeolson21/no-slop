package precheck_test

import (
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/slop/precheck"
)

func TestScanAcquitsSimilarButSafePatterns(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		intent string
		file   precheck.File
	}{
		{
			name: "independent comparison",
			file: precheck.File{Path: "guard.go", AddedContent: "if observed != expected {\n", CurrentContent: "if observed != expected {\n"},
		},
		{
			name: "pre-mutation snapshot",
			file: precheck.File{
				Path:         "cache.go",
				AddedContent: "previousGeneration := c.generation\n\n\n\n",
				CurrentContent: "previousGeneration := c.generation\n" +
					"c.value = next\n" +
					"c.generation++\n" +
					"if c.generation != previousGeneration {}\n",
			},
		},
		{
			name: "comparison is not mutation",
			file: precheck.File{
				Path:         "cache.go",
				AddedContent: "\npreviousGeneration := c.generation\n\n",
				CurrentContent: "if c.generation == expectedGeneration {}\n" +
					"previousGeneration := c.generation\n" +
					"if c.generation != previousGeneration {}\n",
			},
		},
		{
			name: "narrower tolerance",
			file: precheck.File{
				Path:            "money_test.go",
				AddedContent:    "if math.Abs(got-want) > 0.001 {}\n",
				BaselineContent: "if math.Abs(got-want) > 0.02 {}\n",
				CurrentContent:  "if math.Abs(got-want) > 0.001 {}\n",
			},
		},
		{
			name: "independent replacement literal",
			file: precheck.File{
				Path:            "checksum_test.go",
				AddedContent:    "want := uint32(42)\n",
				BaselineContent: "want := uint32(41)\n",
				CurrentContent:  "want := uint32(42)\n",
			},
		},
		{
			name: "comment with fail-closed action",
			file: precheck.File{
				Path:           "policy.go",
				AddedContent:   "// Work around the provider outage by denying access.\nreturn false\n",
				CurrentContent: "// Work around the provider outage by denying access.\nreturn false\n",
			},
		},
		{
			name: "follow-up with durable references",
			file: precheck.File{
				Path:           "handler.go",
				AddedContent:   "// TODO: Filed as #123 and approved under SEC-42.\n",
				CurrentContent: "// TODO: Filed as #123 and approved under SEC-42.\n",
			},
		},
		{
			name: "error fails closed",
			file: precheck.File{
				Path:           "health.go",
				AddedContent:   "\nreturn false\n",
				CurrentContent: "if err != nil {\nreturn false\n}\n",
			},
		},
		{
			name: "identifier containing err is not error context",
			file: precheck.File{
				Path:           "fruit.go",
				AddedContent:   "cherry := enabled\nreturn true\n",
				CurrentContent: "cherry := enabled\nreturn true\n",
			},
		},
		{
			name:   "new file requested by intent",
			intent: "Add the metrics server only.",
			file: precheck.File{
				Path:           "metrics/server.go",
				AddedContent:   "package metrics\n",
				CurrentContent: "package metrics\n",
			},
		},
		{
			name: "explicit permissive policy",
			file: precheck.File{
				Path:           "optional.go",
				AddedContent:   "\n// Optional configuration is fail-open by policy.\nreturn nil, nil\n",
				CurrentContent: "if errors.Is(err, os.ErrNotExist) {\n// Optional configuration is fail-open by policy.\nreturn nil, nil\n}\n",
			},
		},
		{
			name: "independent expected constructor",
			file: precheck.File{
				Path:            "parser_test.go",
				AddedContent:    "want := NewExpected(\"valid\")\n",
				BaselineContent: "want := Expected{Value: \"valid\"}\n",
				CurrentContent:  "got := Parse(\"valid\")\nwant := NewExpected(\"valid\")\n",
			},
		},
		{
			name: "validation on both versioned routes",
			file: precheck.File{
				Path: "routes.go",
				AddedContent: "\nif !valid(req.Body) {}\n\n\n\n" +
					"if !valid(req.Body) {}\n",
				CurrentContent: "r.Post(\"/v1/import\", func(req Request) Response {\n" +
					"if !valid(req.Body) { return BadRequest() }\n" +
					"return importData(req.Body)\n" +
					"})\n" +
					"r.Post(\"/v2/import\", func(req Request) Response {\n" +
					"if !valid(req.Body) { return BadRequest() }\n" +
					"return importData(req.Body)\n" +
					"})\n",
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if findings := precheck.Scan([]precheck.File{testCase.file}, testCase.intent); len(findings) != 0 {
				t.Fatalf("safe pattern produced findings: %+v", findings)
			}
		})
	}
}

func TestScanUsesTheMoreSpecificLensForOverlappingPermissivePatterns(t *testing.T) {
	t.Parallel()

	files := []precheck.File{
		{
			Path: "roles.go",
			AddedContent: "\n\n// availability matters more than freshness\n" +
				"return s.cached[userID], nil\n",
			CurrentContent: "roles, err := s.backend.Load(ctx, userID)\n" +
				"if err != nil {\n" +
				"// availability matters more than freshness\n" +
				"return s.cached[userID], nil\n" +
				"}\n",
		},
		{
			Path:         "loader.go",
			AddedContent: "\n\n\nif errors.Is(err, os.ErrNotExist) {\nreturn nil, nil\n}\n",
			CurrentContent: "if explicit {\nreturn nil, fmt.Errorf(\"read policy: %w\", err)\n}\n" +
				"if errors.Is(err, os.ErrNotExist) {\nreturn nil, nil\n}\n",
		},
	}

	findings := precheck.Scan(files, "")
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want one per file", findings)
	}
	if findings[0].Lens != "rule-applied-in-one-place-not-sibling" || findings[1].Lens != "comment-defended-workaround" {
		t.Fatalf("specific lens selection = %+v", findings)
	}
}

func TestScanRedundantCommentAcquitsConventionalDocumentation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		file precheck.File
	}{
		{
			name: "doc comment that adds information beyond the name",
			file: precheck.File{
				Path:         "scan.go",
				AddedContent: "// Scan runs every conservative lens pre-check.\n\n\n\n",
				CurrentContent: "// Scan runs every conservative lens pre-check.\n" +
					"func Scan(files []File) []Finding {\n" +
					"\treturn nil\n" +
					"}\n",
			},
		},
		{
			name: "markdown heading above a matching term",
			file: precheck.File{
				Path:           "docs/slop-taxonomy.md",
				AddedContent:   "## Redundant comment\n\nName: `redundant-comment`\n",
				CurrentContent: "## Redundant comment\n\nName: `redundant-comment`\n",
			},
		},
		{
			name: "pointer dereference is not a comment",
			file: precheck.File{
				Path:         "store.go",
				AddedContent: "\n*out = value\n\n\n",
				CurrentContent: "func store(out *int, value int) {\n" +
					"\t*out = value\n" +
					"\tlog(out, value)\n" +
					"}\n",
			},
		},
		{
			name: "note separated from the declaration by a blank line",
			file: precheck.File{
				Path:         "header.go",
				AddedContent: "// parseHeader\n\n\n\n\n",
				CurrentContent: "// parseHeader\n" +
					"\n" +
					"func parseHeader(data []byte) Header {\n" +
					"\treturn Header{}\n" +
					"}\n",
			},
		},
		{
			name: "wrapped doc comment opening on the name alone",
			file: precheck.File{
				Path:         "adopt.go",
				AddedContent: "// adoptBranchRef\n// refuses to move the ref off a commit the run never observed.\n\n\n",
				CurrentContent: "// adoptBranchRef\n" +
					"// refuses to move the ref off a commit the run never observed.\n" +
					"func adoptBranchRef(gate string) error {\n" +
					"\treturn nil\n" +
					"}\n",
			},
		},
		{
			name: "repeated stop-word pairing",
			file: precheck.File{
				Path:           "notes.go",
				AddedContent:   "// After the fix, the fix round runs again.\n\n",
				CurrentContent: "// After the fix, the fix round runs again.\nfunc rerun() {}\n",
			},
		},
		{
			name: "repeated phrase qualified by a constraint",
			file: precheck.File{
				Path:           "process.go",
				AddedContent:   "// Call Close before Wait; never call Close before Wait from the callback.\n",
				CurrentContent: "// Call Close before Wait; never call Close before Wait from the callback.\nfunc stop() {}\n",
			},
		},
		{
			name: "repeated phrase with a trailing constraint qualifier",
			file: precheck.File{
				Path:           "process.go",
				AddedContent:   "// Call Close before Wait on success; call Close before Wait on cancellation only after the callback exits.\n",
				CurrentContent: "// Call Close before Wait on success; call Close before Wait on cancellation only after the callback exits.\nfunc stop() {}\n",
			},
		},
		{
			name: "adjacent-code overlap with an ordering qualification",
			file: precheck.File{
				Path:         "retry.go",
				AddedContent: "// Record retry status after timeout attempt.\n\n",
				CurrentContent: "// Record retry status after timeout attempt.\n" +
					"record(retry, status, timeout, attempt)\n",
			},
		},
		{
			name: "adjacent-code overlap with an unlisted invariant",
			file: precheck.File{
				Path:         "cache.go",
				AddedContent: "// Atomically update cache value.\n\n",
				CurrentContent: "// Atomically update cache value.\n" +
					"update(cache, value)\n",
			},
		},
		{
			name: "declaration comment with a return invariant",
			file: precheck.File{
				Path:         "retry.go",
				AddedContent: "// RetryBudget is verified on return.\n\n",
				CurrentContent: "// RetryBudget is verified on return.\n" +
					"var RetryBudget = 3\n",
			},
		},
		{
			name: "repeated phrase preceded by negation",
			file: precheck.File{
				Path:           "process.go",
				AddedContent:   "// Never call Close before Wait. Call Close before Wait.\n",
				CurrentContent: "// Never call Close before Wait. Call Close before Wait.\nfunc stop() {}\n",
			},
		},
		{
			name: "inline comment is not attached to following code",
			file: precheck.File{
				Path:           "counter.go",
				AddedContent:   "i += 1 // increment i\nreturn i\n",
				CurrentContent: "i += 1 // increment i\nreturn i\n",
			},
		},
		{
			name: "unchanged inline comment on edited code",
			file: precheck.File{
				Path:            "cache.go",
				AddedContent:    "newValue := load() /* normalize key before lookup; normalize key before lookup */\n",
				BaselineContent: "oldValue := load() /* normalize key before lookup; normalize key before lookup */\n",
				CurrentContent:  "newValue := load() /* normalize key before lookup; normalize key before lookup */\n",
			},
		},
		{
			name: "indented command sample inside a doc comment",
			file: precheck.File{
				Path: "version.go",
				AddedContent: "// Set via ldflags at build time:\n//\n" +
					"//\t-X internal/buildinfo.Version=v1.0.0 -X internal/buildinfo.Commit=abc\n\n",
				CurrentContent: "// Set via ldflags at build time:\n//\n" +
					"//\t-X internal/buildinfo.Version=v1.0.0 -X internal/buildinfo.Commit=abc\n" +
					"var Version string\n",
			},
		},
		{
			name: "block comment continuation preserves its rationale",
			file: precheck.File{
				Path:         "wire.go",
				AddedContent: "\n\t/* Preserve byte order\n\t * Preserve byte order because signatures cover the wire representation.\n\t */\n\n",
				CurrentContent: "func signedBytes(payload []byte) []byte {\n" +
					"\t/* Preserve byte order\n" +
					"\t * Preserve byte order because signatures cover the wire representation.\n" +
					"\t */\n" +
					"\treturn payload\n" +
					"}\n",
			},
		},
		{
			name: "indented command sample inside a block comment",
			file: precheck.File{
				Path:         "setup.go",
				AddedContent: "/*\n * Example:\n *     git config user name alice && git config user name bob\n */\n\n",
				CurrentContent: "/*\n" +
					" * Example:\n" +
					" *     git config user name alice && git config user name bob\n" +
					" */\n" +
					"func configure() {}\n",
			},
		},
		{
			name: "comment-shaped lines inside a raw string",
			file: precheck.File{
				Path:         "fixture.go",
				AddedContent: "\n// increment i\ni += 1\n\n\n",
				CurrentContent: "var fixture = `\n" +
					"// increment i\n" +
					"i += 1\n" +
					"`\n" +
					"func advance(i int) int { return i + 1 }\n",
			},
		},
		{
			name: "raw string after a closed block comment",
			file: precheck.File{
				Path:         "fixture.go",
				AddedContent: "\n// normalize key before lookup; normalize key before lookup\n\n",
				CurrentContent: "/* fixture */ var fixture = `\n" +
					"// normalize key before lookup; normalize key before lookup\n" +
					"`\n",
			},
		},
		{
			name: "comment-shaped lines inside a triple-quoted string",
			file: precheck.File{
				Path:         "fixture.py",
				AddedContent: "\n# increment i\ni += 1\n\n",
				CurrentContent: "fixture = \"\"\"\n" +
					"# increment i\n" +
					"i += 1\n" +
					"\"\"\"\n",
			},
		},
		{
			name: "unsupported Rust raw string",
			file: precheck.File{
				Path:         "fixture.rs",
				AddedContent: "\n// normalize key before lookup; normalize key before lookup\n\n\n",
				CurrentContent: "const FIXTURE: &str = r#\"\n" +
					"// normalize key before lookup; normalize key before lookup\n" +
					"\"#;\n",
			},
		},
		{
			name: "unsupported shell heredoc",
			file: precheck.File{
				Path:         "fixture.sh",
				AddedContent: "\n# normalize key before lookup; normalize key before lookup\n\n\n",
				CurrentContent: "cat <<'TEXT'\n" +
					"# normalize key before lookup; normalize key before lookup\n" +
					"TEXT\n",
			},
		},
		{
			name: "unsupported SQL dollar-quoted string",
			file: precheck.File{
				Path:         "fixture.sql",
				AddedContent: "\n-- normalize key before lookup; normalize key before lookup\n\n\n",
				CurrentContent: "SELECT $body$\n" +
					"-- normalize key before lookup; normalize key before lookup\n" +
					"$body$;\n",
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if findings := precheck.Scan([]precheck.File{testCase.file}, ""); len(findings) != 0 {
				t.Fatalf("conventional comment produced findings: %+v", findings)
			}
		})
	}
}

func TestScanFlagsRedundantCommentShapes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		file        precheck.File
		line        int
		description string
	}{
		{
			name: "doc comment adding nothing beyond the declaration name",
			file: precheck.File{
				Path:         "parser_test.go",
				AddedContent: "// TestRejectsEmptyInput verifies the empty-input behavior.\n\n\n\n",
				CurrentContent: "// TestRejectsEmptyInput verifies the empty-input behavior.\n" +
					"func TestRejectsEmptyInput(t *testing.T) {\n" +
					"\tt.Fatal(\"fixture\")\n" +
					"}\n",
			},
			line:        1,
			description: "doc comment adds no information beyond the declaration name",
		},
		{
			name: "phrase restated immediately within one comment",
			file: precheck.File{
				Path:         "cache.go",
				AddedContent: "// normalizeKey removes ASCII padding before lookup; removes ASCII padding before lookup.\n\n\n\n",
				CurrentContent: "// normalizeKey removes ASCII padding before lookup; removes ASCII padding before lookup.\n" +
					"func normalizeKey(value string) string {\n" +
					"\treturn value\n" +
					"}\n",
			},
			line:        1,
			description: "comment repeats a phrase internally",
		},
		{
			name: "comment restating the statement below it",
			file: precheck.File{
				Path:         "counter.go",
				AddedContent: "\n// increment i\n\n\n\n",
				CurrentContent: "func advance(i int) int {\n" +
					"\t// increment i\n" +
					"\ti += 1\n" +
					"\treturn i\n" +
					"}\n",
			},
			line:        2,
			description: "comment restates the adjacent code",
		},
		{
			name: "comment restating code with a trailing comment",
			file: precheck.File{
				Path:         "counter.go",
				AddedContent: "\n// increment i\n\n",
				CurrentContent: "func advance(i int) int {\n" +
					"\t// increment i\n" +
					"\ti += 1 // advance counter\n" +
					"\treturn i\n" +
					"}\n",
			},
			line:        2,
			description: "comment restates the adjacent code",
		},
		{
			name: "comment restating code after a block comment",
			file: precheck.File{
				Path:         "counter.go",
				AddedContent: "\n// increment i\n\n",
				CurrentContent: "func advance(i int) int {\n" +
					"\t// increment i\n" +
					"\t/* preserve ordering */ i += 1\n" +
					"\treturn i\n" +
					"}\n",
			},
			line:        2,
			description: "comment restates the adjacent code",
		},
		{
			name: "method doc comment adding nothing beyond the method name",
			file: precheck.File{
				Path:         "reader.go",
				AddedContent: "// Close handles the Close case.\n\n\n",
				CurrentContent: "// Close handles the Close case.\n" +
					"func (r *Reader) Close() error {\n" +
					"\treturn nil\n" +
					"}\n",
			},
			line:        1,
			description: "doc comment adds no information beyond the declaration name",
		},
		{
			name: "type doc comment adding nothing beyond the type name",
			file: precheck.File{
				Path:         "reader.go",
				AddedContent: "// Reader handles Reader values.\n\n",
				CurrentContent: "// Reader handles Reader values.\n" +
					"type Reader struct{}\n",
			},
			line:        1,
			description: "doc comment adds no information beyond the declaration name",
		},
		{
			name: "variable doc comment adding nothing beyond the variable name",
			file: precheck.File{
				Path:         "state.go",
				AddedContent: "// DefaultState is the DefaultState value.\n\n",
				CurrentContent: "// DefaultState is the DefaultState value.\n" +
					"var DefaultState = State{}\n",
			},
			line:        1,
			description: "doc comment adds no information beyond the declaration name",
		},
		{
			name: "constant doc comment adding nothing beyond the constant name",
			file: precheck.File{
				Path:         "limits.go",
				AddedContent: "// MaxRetries is the MaxRetries value.\n\n",
				CurrentContent: "// MaxRetries is the MaxRetries value.\n" +
					"const MaxRetries = 3\n",
			},
			line:        1,
			description: "doc comment adds no information beyond the declaration name",
		},
		{
			name: "multi-name variable doc comment",
			file: precheck.File{
				Path:         "limits.go",
				AddedContent: "// Min and Max are Min and Max values.\n\n",
				CurrentContent: "// Min and Max are Min and Max values.\n" +
					"var Min, Max = 1, 10\n",
			},
			line:        1,
			description: "doc comment adds no information beyond the declaration name",
		},
		{
			name: "physical line after line directive",
			file: precheck.File{
				Path:         "generated.go",
				AddedContent: "\n// normalizeKey removes ASCII padding before lookup; removes ASCII padding before lookup.\n\n",
				CurrentContent: "//line synthetic.go:1000\n" +
					"// normalizeKey removes ASCII padding before lookup; removes ASCII padding before lookup.\n" +
					"func normalizeKey(value string) string { return value }\n",
			},
			line:        2,
			description: "comment repeats a phrase internally",
		},
		{
			name: "grouped type doc comment",
			file: precheck.File{
				Path:         "reader.go",
				AddedContent: "\n// Reader handles Reader values.\n\n\n",
				CurrentContent: "type (\n" +
					"\t// Reader handles Reader values.\n" +
					"\tReader struct{}\n" +
					")\n",
			},
			line:        2,
			description: "doc comment adds no information beyond the declaration name",
		},
		{
			name: "grouped variable doc comment",
			file: precheck.File{
				Path:         "state.go",
				AddedContent: "\n// DefaultState is the DefaultState value.\n\n\n",
				CurrentContent: "var (\n" +
					"\t// DefaultState is the DefaultState value.\n" +
					"\tDefaultState = State{}\n" +
					")\n",
			},
			line:        2,
			description: "doc comment adds no information beyond the declaration name",
		},
		{
			name: "grouped constant doc comment",
			file: precheck.File{
				Path:         "limits.go",
				AddedContent: "\n// MaxRetries is the MaxRetries value.\n\n\n",
				CurrentContent: "const (\n" +
					"\t// MaxRetries is the MaxRetries value.\n" +
					"\tMaxRetries = 3\n" +
					")\n",
			},
			line:        2,
			description: "doc comment adds no information beyond the declaration name",
		},
		{
			name: "inline comment repeats a phrase internally",
			file: precheck.File{
				Path:           "cache.go",
				AddedContent:   "value := cache[key] // normalize key before lookup; normalize key before lookup.\n",
				CurrentContent: "value := cache[key] // normalize key before lookup; normalize key before lookup.\n",
			},
			line:        1,
			description: "comment repeats a phrase internally",
		},
		{
			name: "first of multiple inline comments repeats a phrase",
			file: precheck.File{
				Path:           "cache.go",
				AddedContent:   "/* normalize key before lookup; normalize key before lookup. */ value := 1 /* rationale */\n",
				CurrentContent: "/* normalize key before lookup; normalize key before lookup. */ value := 1 /* rationale */\n",
			},
			line:        1,
			description: "comment repeats a phrase internally",
		},
		{
			name: "new duplicate inline comment beside existing copy",
			file: precheck.File{
				Path:            "cache.go",
				AddedContent:    "copy := cache[key] // normalize key before lookup; normalize key before lookup.\n\n",
				BaselineContent: "value := cache[key] // normalize key before lookup; normalize key before lookup.\n",
				CurrentContent: "copy := cache[key] // normalize key before lookup; normalize key before lookup.\n" +
					"value := cache[key] // normalize key before lookup; normalize key before lookup.\n",
			},
			line:        1,
			description: "comment repeats a phrase internally",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			findings := precheck.Scan([]precheck.File{testCase.file}, "")
			if len(findings) != 1 {
				t.Fatalf("findings = %+v, want exactly one", findings)
			}
			if findings[0].Lens != "redundant-comment" || findings[0].Line != testCase.line || findings[0].Description != testCase.description {
				t.Fatalf("finding = %+v, want redundant-comment at line %d describing %q", findings[0], testCase.line, testCase.description)
			}
		})
	}
}
