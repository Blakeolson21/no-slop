package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/slop/corpus"
	"github.com/kunchenguid/no-mistakes/internal/slop/prose"
)

type campaign struct {
	Cases []campaignCase `json:"cases"`
}

type campaignCase struct {
	CaseID             string        `json:"case_id"`
	Alias              string        `json:"alias"`
	Tier               string        `json:"tier"`
	Intent             string        `json:"intent"`
	ConditioningLenses []string      `json:"conditioning_lenses"`
	ThreadFixture      *prose.Thread `json:"thread_fixture,omitempty"`
}

type caseOutput struct {
	CaseID    string           `json:"case_id"`
	Alias     string           `json:"alias"`
	Tier      string           `json:"tier"`
	Findings  []corpus.Finding `json:"findings"`
	ElapsedMS int64            `json:"elapsed_ms"`
}

func main() {
	corpusRoot := flag.String("corpus", "corpus/seeds", "recorded corpus root")
	campaignPath := flag.String("campaign", "corpus/campaign.json", "campaign manifest")
	flag.Parse()

	encoded, err := os.ReadFile(*campaignPath)
	if err != nil {
		fatal(err)
	}
	var manifest campaign
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		fatal(err)
	}
	var output []caseOutput
	for _, testCase := range manifest.Cases {
		directory, err := caseDirectory(*corpusRoot, testCase.Alias)
		if err != nil {
			fatal(err)
		}
		diff, err := os.ReadFile(filepath.Join(directory, "change.diff"))
		if err != nil {
			fatal(err)
		}
		started := time.Now()
		findings, err := corpus.ReplayMandatory(context.Background(), diff, corpus.ReplayOptions{
			Intent: testCase.Intent,
			Thread: testCase.ThreadFixture,
		})
		if err != nil {
			fatal(fmt.Errorf("replay %s: %w", testCase.Alias, err))
		}
		if findings == nil {
			findings = []corpus.Finding{}
		}
		output = append(output, caseOutput{
			CaseID:    testCase.CaseID,
			Alias:     testCase.Alias,
			Tier:      testCase.Tier,
			Findings:  findings,
			ElapsedMS: time.Since(started).Milliseconds(),
		})
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(map[string]any{"cases": output}); err != nil {
		fatal(err)
	}
}

func caseDirectory(root, alias string) (string, error) {
	number, err := strconv.Atoi(strings.TrimPrefix(alias, "case-"))
	if err != nil {
		return "", fmt.Errorf("parse alias %s: %w", alias, err)
	}
	matches, err := filepath.Glob(filepath.Join(root, fmt.Sprintf("%02d-*", number)))
	if err != nil {
		return "", err
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("alias %s resolved to %d case directories", alias, len(matches))
	}
	return matches[0], nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
