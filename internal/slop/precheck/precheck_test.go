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
