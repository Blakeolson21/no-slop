package db

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/Blakeolson21/no-slop/internal/types"
)

// Stats summarizes historical no-slop usage across all repositories.
type Stats struct {
	TotalRepos       int
	TotalRuns        int
	PullRequests     int
	RescueRuns       int
	ReportedFindings int
	FixedFindings    int
	StepStats        []StepStats
	RepoStats        []RepoStats
}

// StepStats summarizes reported and fixed findings for one pipeline step.
type StepStats struct {
	StepName         types.StepName
	ReportedFindings int
	FixedFindings    int
}

// RepoStats summarizes historical usage for one repository.
type RepoStats struct {
	RepoID           string
	WorkingPath      string
	Runs             int
	RescueRuns       int
	ReportedFindings int
	FixedFindings    int
}

// DisplayName returns a compact repository name for terminal reports.
func (r RepoStats) DisplayName() string {
	name := filepath.Base(r.WorkingPath)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return r.WorkingPath
	}
	return name
}

// GetStats aggregates historical usage across all repositories.
func (d *DB) GetStats() (*Stats, error) {
	repos, err := d.getRepos()
	if err != nil {
		return nil, err
	}

	stats := &Stats{TotalRepos: len(repos)}
	stepStats := map[types.StepName]*StepStats{}

	for _, repo := range repos {
		repoStats := RepoStats{RepoID: repo.ID, WorkingPath: repo.WorkingPath}
		runs, err := d.GetRunsByRepo(repo.ID)
		if err != nil {
			return nil, err
		}
		repoStats.Runs = len(runs)
		stats.TotalRuns += len(runs)

		for _, run := range runs {
			if run.PRURL != nil && *run.PRURL != "" {
				stats.PullRequests++
			}

			runReported, runFixed, err := d.aggregateRunStats(run.ID, stepStats)
			if err != nil {
				return nil, err
			}
			stats.ReportedFindings += runReported
			stats.FixedFindings += runFixed
			repoStats.ReportedFindings += runReported
			repoStats.FixedFindings += runFixed
			if runReported > 0 && runFixed > 0 {
				stats.RescueRuns++
				repoStats.RescueRuns++
			}
		}

		stats.RepoStats = append(stats.RepoStats, repoStats)
	}

	for _, step := range stepStats {
		if step.ReportedFindings == 0 && step.FixedFindings == 0 {
			continue
		}
		stats.StepStats = append(stats.StepStats, *step)
	}
	sortStepStats(stats.StepStats)
	sortRepoStats(stats.RepoStats)

	return stats, nil
}

func (d *DB) aggregateRunStats(runID string, stepStats map[types.StepName]*StepStats) (int, int, error) {
	steps, err := d.GetStepsByRun(runID)
	if err != nil {
		return 0, 0, err
	}

	runReported := 0
	runFixed := 0
	for _, step := range steps {
		rounds, err := d.GetRoundsByStep(step.ID)
		if err != nil {
			return 0, 0, err
		}
		findingStats := stepFindingStats(step, rounds)
		reported, fixed := findingStats.ReportedFindings, findingStats.FixedFindings

		runReported += reported
		runFixed += fixed
		stat := stepStats[step.StepName]
		if stat == nil {
			stat = &StepStats{StepName: step.StepName}
			stepStats[step.StepName] = stat
		}
		stat.ReportedFindings += reported
		stat.FixedFindings += fixed
	}

	return runReported, runFixed, nil
}

func stepFindingCounts(step *StepResult, rounds []*StepRound) (reported int, final int) {
	stats := stepFindingStats(step, rounds)
	return stats.ReportedFindings, stats.ReportedFindings - stats.FixedFindings
}

func stepFindingStats(step *StepResult, rounds []*StepRound) StepStats {
	stats := StepStats{StepName: step.StepName}
	if len(rounds) == 0 {
		count := findingsCount(step.FindingsJSON)
		stats.ReportedFindings = count
		return stats
	}
	if step.StepName != types.StepReview {
		return structuralStepFindingStats(step, rounds)
	}

	reportedLineages := make(map[string]bool)
	var reportedLegacy []types.Finding
	var activeLegacy []types.Finding
	var current []types.Finding
	for _, round := range rounds {
		items := findingItems(round.FindingsJSON)
		current = appendPendingUserFindings(items, round.UserFindingsJSON, true)
		legacy := make([]types.Finding, 0, len(current))
		for _, item := range current {
			if key, ok := findingStatsLineageKey(item, true); ok {
				reportedLineages[key] = true
				continue
			}
			legacy = append(legacy, item)
		}
		reportedLegacy = mergeLegacyFindingOccurrences(reportedLegacy, activeLegacy, legacy)
		activeLegacy = legacy
	}

	stats.ReportedFindings = len(reportedLineages) + len(reportedLegacy)
	currentCount := len(current)
	stats.FixedFindings = stats.ReportedFindings - currentCount
	if stats.FixedFindings < 0 {
		stats.FixedFindings = 0
	}
	if stats.FixedFindings > stats.ReportedFindings {
		stats.FixedFindings = stats.ReportedFindings
	}
	return stats
}

func structuralStepFindingStats(step *StepResult, rounds []*StepRound) StepStats {
	reported := make(map[types.FindingIdentity]bool)
	reportedCounts := make(map[types.FindingIdentity]int)
	var current []types.Finding
	for _, round := range rounds {
		current = findingItems(round.FindingsJSON)
		currentCounts := types.CountFindingFingerprints(current)
		for _, item := range current {
			if reported[item.Identity()] || (currentCounts[item.Fingerprint()] == 1 && reportedCounts[item.Fingerprint()] == 1) {
				continue
			}
			reported[item.Identity()] = true
			reportedCounts[item.Fingerprint()]++
		}
	}
	stats := StepStats{StepName: step.StepName, ReportedFindings: len(reported)}
	stats.FixedFindings = stats.ReportedFindings - len(current)
	if stats.FixedFindings < 0 {
		stats.FixedFindings = 0
	}
	return stats
}

func findingStatsLineageKey(item types.Finding, lineageStats bool) (string, bool) {
	if !lineageStats || !item.HasLineage() {
		return "", false
	}
	return findingLineageStatsKey(item), true
}

func findingLineageStatsKey(item types.Finding) string {
	return "lineage\x00" + item.ID + "\x00" + item.ContinuityToken
}

func appendPendingUserFindings(current []types.Finding, raw *string, lineageStats bool) []types.Finding {
	if !lineageStats {
		return current
	}
	seen := make(map[string]bool, len(current))
	for _, item := range current {
		if item.HasLineage() {
			seen[findingLineageStatsKey(item)] = true
		}
	}
	for _, item := range findingItems(raw) {
		if item.Source != types.FindingSourceUser || !item.HasLineage() {
			continue
		}
		key := findingLineageStatsKey(item)
		if seen[key] {
			continue
		}
		seen[key] = true
		current = append(current, item)
	}
	return current
}

func mergeLegacyFindingOccurrences(reported, active, current []types.Finding) []types.Finding {
	activeMatched := make([]bool, len(active))
	currentMatched := make([]bool, len(current))
	activeOccurrences := make(map[string][]int, len(active))
	currentOccurrences := make(map[string][]int, len(current))
	for i, item := range active {
		if item.HasOccurrence() {
			activeOccurrences[item.OccurrenceToken] = append(activeOccurrences[item.OccurrenceToken], i)
		}
	}
	for i, item := range current {
		if item.HasOccurrence() {
			currentOccurrences[item.OccurrenceToken] = append(currentOccurrences[item.OccurrenceToken], i)
		}
	}
	for token, currentIndexes := range currentOccurrences {
		activeIndexes := activeOccurrences[token]
		if len(currentIndexes) == 1 && len(activeIndexes) == 1 {
			currentMatched[currentIndexes[0]] = true
			activeMatched[activeIndexes[0]] = true
		}
	}

	activeExact := make(map[types.FindingIdentity][]int, len(active))
	currentExact := make(map[types.FindingIdentity][]int, len(current))
	for i, item := range active {
		if !activeMatched[i] {
			activeExact[item.Identity()] = append(activeExact[item.Identity()], i)
		}
	}
	for i, item := range current {
		if !currentMatched[i] {
			currentExact[item.Identity()] = append(currentExact[item.Identity()], i)
		}
	}
	for identity, currentIndexes := range currentExact {
		activeIndexes := activeExact[identity]
		if len(currentIndexes) == 1 && len(activeIndexes) == 1 {
			currentMatched[currentIndexes[0]] = true
			activeMatched[activeIndexes[0]] = true
		}
	}

	activeFingerprint := make(map[types.FindingIdentity][]int)
	currentFingerprint := make(map[types.FindingIdentity][]int)
	for i, item := range active {
		if !activeMatched[i] {
			activeFingerprint[item.Fingerprint()] = append(activeFingerprint[item.Fingerprint()], i)
		}
	}
	for i, item := range current {
		if !currentMatched[i] {
			currentFingerprint[item.Fingerprint()] = append(currentFingerprint[item.Fingerprint()], i)
		}
	}
	for fingerprint, currentIndexes := range currentFingerprint {
		activeIndexes := activeFingerprint[fingerprint]
		if len(currentIndexes) == 1 && len(activeIndexes) == 1 {
			currentMatched[currentIndexes[0]] = true
			activeMatched[activeIndexes[0]] = true
		}
	}
	for i, item := range current {
		if !currentMatched[i] {
			reported = append(reported, item)
		}
	}
	return reported
}

// FixedFindingsByStep returns how many findings were resolved for a single step.
func (d *DB) FixedFindingsByStep(step *StepResult) (int, error) {
	stats, err := d.StepFindingStats(step)
	if err != nil {
		return 0, err
	}
	return stats.FixedFindings, nil
}

// StepFindingStats returns reported and fixed finding counts for a single step.
func (d *DB) StepFindingStats(step *StepResult) (StepStats, error) {
	rounds, err := d.GetRoundsByStep(step.ID)
	if err != nil {
		return StepStats{}, err
	}
	return stepFindingStats(step, rounds), nil
}

func findingsCount(raw *string) int {
	if raw == nil || *raw == "" {
		return 0
	}
	findings, err := types.ParseFindingsJSON(*raw)
	if err != nil {
		return 0
	}
	return len(findings.Items)
}

func findingItems(raw *string) []types.Finding {
	if raw == nil || *raw == "" {
		return nil
	}
	findings, err := types.ParseFindingsJSON(*raw)
	if err != nil {
		return nil
	}
	return findings.Items
}

func sortStepStats(stats []StepStats) {
	slices.SortFunc(stats, func(a, b StepStats) int {
		if a.FixedFindings != b.FixedFindings {
			return b.FixedFindings - a.FixedFindings
		}
		if a.ReportedFindings != b.ReportedFindings {
			return b.ReportedFindings - a.ReportedFindings
		}
		return a.StepName.Order() - b.StepName.Order()
	})
}

func sortRepoStats(stats []RepoStats) {
	slices.SortFunc(stats, func(a, b RepoStats) int {
		if a.RescueRuns != b.RescueRuns {
			return b.RescueRuns - a.RescueRuns
		}
		if a.FixedFindings != b.FixedFindings {
			return b.FixedFindings - a.FixedFindings
		}
		if a.Runs != b.Runs {
			return b.Runs - a.Runs
		}
		if a.WorkingPath < b.WorkingPath {
			return -1
		}
		if a.WorkingPath > b.WorkingPath {
			return 1
		}
		return 0
	})
}

func (d *DB) getRepos() ([]*Repo, error) {
	rows, err := d.sql.Query(`SELECT id, working_path, upstream_url, COALESCE(fork_url, ''), default_branch, created_at FROM repos ORDER BY working_path`)
	if err != nil {
		return nil, fmt.Errorf("get repos: %w", err)
	}
	defer rows.Close()

	var repos []*Repo
	for rows.Next() {
		repo := &Repo{}
		if err := rows.Scan(&repo.ID, &repo.WorkingPath, &repo.UpstreamURL, &repo.ForkURL, &repo.DefaultBranch, &repo.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan repo: %w", err)
		}
		repos = append(repos, repo)
	}
	return repos, rows.Err()
}
