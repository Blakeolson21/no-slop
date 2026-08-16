package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/config"
	slopcli "github.com/Blakeolson21/no-slop/internal/slop/cli"
	"github.com/Blakeolson21/no-slop/internal/slop/engine"
	"github.com/Blakeolson21/no-slop/internal/slop/provenance"
)

// The base line names the remote this run asked, and the remote a repository is
// configured with routinely carries userinfo. Every reason the resolution route
// builds is printed there AND stored in the append-only provenance record, so a
// credential that reaches one of them cannot be taken back: the store's whole
// design is that nothing evicts a record. The success path was redacted from the
// start and git redacts its own error text, which left an ordinary ls-remote
// failure - an expired token, an offline runner, a missing refs/heads/main - as
// the one route that put a live token on stdout, twice, once per branch tried.

const credentialToken = "ghp_examplenottherealtoken"

// recordingStore captures what a run would have appended, so the test can ask
// the same question of the provenance record that it asks of stdout.
type recordingStore struct{ records []provenance.Record }

func (s *recordingStore) Window(string, string) ([]provenance.Record, error) { return nil, nil }

func (s *recordingStore) HasIdentifiedHistory() (bool, error) { return false, nil }

func (s *recordingStore) Append(record provenance.Record) error {
	s.records = append(s.records, record)
	return nil
}

// TestAnUnreachableCredentialledRemoteIsRedactedEverywhereItIsReported drives
// the failing half of the ls-remote route. Port 1 on the loopback interface
// refuses immediately, so every attempt fails for an ordinary reason and every
// reason names the remote.
func TestAnUnreachableCredentialledRemoteIsRedactedEverywhereItIsReported(t *testing.T) {
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# Project\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial")
	runGit(t, dir, "remote", "add", "origin", "https://someone:"+credentialToken+"@127.0.0.1:1/owner/repo.git")
	runGit(t, dir, "switch", "-c", "docs/notes")
	writeFile(t, dir, "README.md", "# Project\n\nPlain update.\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "notes")

	store := &recordingStore{}
	var stdout, stderr bytes.Buffer
	code := slopcli.Run(context.Background(), []string{"gate", "--repo", dir}, &stdout, &stderr, slopcli.Options{
		ReviewerFactory: func(context.Context, *config.Config, io.Writer) (engine.Reviewer, io.Closer, error) {
			return nil, nil, errors.New("no runnable agent found")
		},
		ProvenanceStore: store,
	})
	output := stdout.String() + stderr.String()
	if code == 0 {
		t.Fatalf("exit = 0: a run that could not verify its base must not pass\n%s", output)
	}
	if strings.Contains(output, credentialToken) {
		t.Fatalf("the run printed the remote's credential:\n%s", output)
	}
	// The remote must still be named, or the assertion above would hold for a
	// run that simply stopped reporting which remote it asked.
	if !strings.Contains(output, "127.0.0.1") {
		t.Fatalf("the run does not name the remote it asked:\n%s", output)
	}
	recorded, err := json.Marshal(store.records)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.records) == 0 {
		t.Fatalf("the run recorded nothing, so this test cannot see what the provenance store would hold:\n%s", output)
	}
	if strings.Contains(string(recorded), credentialToken) {
		t.Fatalf("the credential reached the append-only provenance record: %s", recorded)
	}
}
