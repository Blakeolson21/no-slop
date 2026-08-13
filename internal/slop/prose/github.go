package prose

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/shellenv"
)

// GHThreadReader reads live GitHub issue and pull-request state through gh.
type GHThreadReader struct {
	executable string
}

// NewGHThreadReader constructs a live thread reader. An empty executable uses
// gh from PATH.
func NewGHThreadReader(executable string) *GHThreadReader {
	if executable == "" {
		executable = "gh"
	}
	return &GHThreadReader{executable: executable}
}

// Read fetches the thread state and all comments returned by gh.
func (r *GHThreadReader) Read(ctx context.Context, threadURL string) (Thread, error) {
	kind, err := githubThreadKind(threadURL)
	if err != nil {
		return Thread{}, err
	}
	cmd := exec.CommandContext(ctx, r.executable, kind, "view", threadURL, "--json", "state,comments")
	shellenv.ConfigureShellCommand(cmd)
	output, err := shellenv.OutputShellCommand(cmd)
	if err != nil {
		return Thread{}, fmt.Errorf("gh %s view: %w", kind, err)
	}
	var payload struct {
		State    string `json:"state"`
		Comments []struct {
			Body string `json:"body"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		return Thread{}, fmt.Errorf("parse gh thread output: %w", err)
	}
	thread := Thread{Open: strings.EqualFold(payload.State, "open")}
	for _, comment := range payload.Comments {
		thread.Comments = append(thread.Comments, comment.Body)
	}
	return thread, nil
}

func githubThreadKind(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") {
		return "", fmt.Errorf("thread URL must be an https://github.com issue or pull request")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[3] == "" {
		return "", fmt.Errorf("thread URL must identify one issue or pull request")
	}
	switch parts[2] {
	case "issues":
		return "issue", nil
	case "pull":
		return "pr", nil
	default:
		return "", fmt.Errorf("thread URL must identify one issue or pull request")
	}
}
