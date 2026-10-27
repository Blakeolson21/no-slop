package gate

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/filelock"
	"github.com/Blakeolson21/no-slop/internal/git"
	"github.com/Blakeolson21/no-slop/internal/paths"
)

const objectStoreKey = "no-slop.objectStore"

// UpstreamIdentity canonicalizes transport spelling without equating unrelated
// servers or case-sensitive repository paths. Credentials never enter the key.
// SSH user and nondefault ports remain significant (they can select a different
// repository); HTTP(S) userinfo is authentication only. Transport protocols are
// deliberately distinct, as on the fleet's existing object-store helper.
func UpstreamIdentity(baseDir, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\x00\r\n") {
		return "", fmt.Errorf("empty or malformed upstream URL")
	}
	if !strings.Contains(raw, "://") && !filepath.IsAbs(raw) && strings.Contains(raw, "::") {
		return "", fmt.Errorf("unsupported upstream remote helper")
	}
	if !strings.Contains(raw, "://") && !filepath.IsAbs(raw) {
		if colon := strings.IndexByte(raw, ':'); colon > 0 && !strings.ContainsAny(raw[:colon], "/\\") && !(len(raw) > 2 && raw[1] == ':' && (raw[2] == '\\' || raw[2] == '/')) {
			raw = "ssh://" + raw[:colon] + "/" + raw[colon+1:]
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("malformed upstream URL")
	}
	if u.Scheme == "" || u.Scheme == "file" {
		local, ok := workingRemoteLocalPath(baseDir, raw)
		if !ok {
			return "", fmt.Errorf("unsupported local upstream URL")
		}
		return (&url.URL{Scheme: "file", Path: filepath.ToSlash(canonicalWorkingRemotePath(local))}).String(), nil
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" && scheme != "ssh" && scheme != "git" {
		return "", fmt.Errorf("unsupported upstream URL scheme")
	}
	if u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("upstream URL requires a host and no query or fragment")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") || (scheme == "ssh" && port == "22") || (scheme == "git" && port == "9418") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	path := strings.TrimSuffix(strings.TrimRight(u.Path, "/"), ".git")
	if path == "" {
		return "", fmt.Errorf("upstream URL has no repository path")
	}
	normalized := &url.URL{Scheme: scheme, Host: host, Path: path}
	if scheme == "ssh" && u.User != nil {
		normalized.User = url.User(u.User.Username())
	}
	return normalized.String(), nil
}

// UpstreamStoreDir returns the durable object pool for this upstream. Lane gate
// directories remain separate: they own credentials, hooks, refs and worktree
// identity, but borrow their common history from this one upstream-keyed pool.
func UpstreamStoreDir(p *paths.Paths, baseDir, upstream string) (string, error) {
	identity, err := UpstreamIdentity(baseDir, upstream)
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte(identity))
	return filepath.Abs(filepath.Join(p.Root(), "stores", fmt.Sprintf("%x.git", key)))
}

// attachUpstreamStore is only for NEW gates. Existing per-path gates must go
// through the explicit migration; init refresh never migrates their objects.
func attachUpstreamStore(ctx context.Context, p *paths.Paths, gateDir, cloneDir, upstream string) error {
	store, err := UpstreamStoreDir(p, cloneDir, upstream)
	if err != nil {
		return err
	}
	if strings.ContainsAny(store, "\r\n") {
		return fmt.Errorf("object store path contains a newline")
	}
	if err := os.MkdirAll(filepath.Dir(store), 0o755); err != nil {
		return err
	}
	lock, err := filelock.AcquireContext(ctx, store+".lock")
	if err != nil {
		return err
	}
	defer lock.Release()

	if err := git.InitBare(ctx, store); err != nil {
		return err
	}
	// No pruning or automatic maintenance while gates borrow objects. Every
	// imported tip also gets a permanent content-addressed root, so even an
	// explicit ordinary gc/repack cannot strand borrowers after force pushes.
	for _, pair := range [][2]string{{"core.longpaths", "true"}, {"gc.auto", "0"}, {"maintenance.auto", "false"}, {"gc.pruneExpire", "never"}, {"gc.reflogExpire", "never"}, {"gc.reflogExpireUnreachable", "never"}} {
		if _, err := git.RunBare(ctx, store, "config", pair[0], pair[1]); err != nil {
			return err
		}
	}
	tips, err := git.Run(ctx, cloneDir, "for-each-ref", "--format=%(objectname)", "refs/heads", "refs/remotes", "refs/tags")
	if err != nil {
		return err
	}
	if err := importStoreObjects(ctx, store, cloneDir, strings.Fields(tips), ""); err != nil {
		return err
	}
	info := filepath.Join(gateDir, "objects", "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(info, "alternates"), []byte(filepath.Join(store, "objects")+"\n"), 0o644); err != nil {
		return err
	}
	if _, err := git.RunBare(ctx, gateDir, "config", objectStoreKey, store); err != nil {
		return err
	}
	// Repacking a borrower without --local would recreate the upstream history
	// in every lane. Automatic maintenance is disabled on both sides.
	for _, pair := range [][2]string{{"gc.auto", "0"}, {"maintenance.auto", "false"}} {
		if _, err := git.RunBare(ctx, gateDir, "config", pair[0], pair[1]); err != nil {
			return err
		}
	}
	return nil
}

func importStoreObjects(ctx context.Context, store, source string, objects []string, runID string) error {
	unique := map[string]bool{}
	for _, oid := range objects {
		if !validObjectID(oid) {
			return fmt.Errorf("invalid object ID for upstream retention")
		}
		unique[oid] = true
	}
	sorted := make([]string, 0, len(unique))
	for oid := range unique {
		sorted = append(sorted, oid)
	}
	sort.Strings(sorted)
	// Bound argv even for repositories with many refs; Git owns atomic ref locks.
	for start := 0; start < len(sorted); start += 128 {
		end := min(start+128, len(sorted))
		args := []string{"-c", "core.hooksPath=", "fetch", "--atomic", "--no-tags", "--no-write-fetch-head", "--no-auto-maintenance", source}
		for _, oid := range sorted[start:end] {
			args = append(args, oid+":refs/retained/"+oid)
			if runID != "" {
				args = append(args, oid+":refs/runs/"+runID+"/"+oid)
			}
		}
		if _, err := git.RunBare(ctx, store, args...); err != nil {
			return fmt.Errorf("retain upstream objects: %w", err)
		}
	}
	return nil
}

func validObjectID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	for _, r := range oid {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return strings.Trim(oid, "0") != ""
}

// RetainRun pins each known run generation in its upstream pool before the run
// worktree is created or removed. Legacy gates remain self-contained. Mutable
// branch names never identify a run's retention roots, so simultaneous lanes
// using the same branch cannot overwrite one another's commits.
func RetainRun(ctx context.Context, p *paths.Paths, run *db.Run) error {
	return RetainRunInGate(ctx, p, p.RepoDir(run.RepoID), run)
}

// RetainRunInGate also accepts phase-specific commits at an executor checkpoint.
// The explicit gate path is the same one used for reviewed-tree evidence.
func RetainRunInGate(ctx context.Context, p *paths.Paths, gateDir string, run *db.Run, extraObjects ...string) error {
	store, err := git.RunBare(ctx, gateDir, "config", "--get", objectStoreKey)
	if err != nil {
		// git config --get exits 1 for an absent legacy key. Other failures
		// must not silently disable retention.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil
		}
		return err
	}
	storesDir, err := filepath.Abs(filepath.Join(p.Root(), "stores"))
	if err != nil {
		return err
	}
	if filepath.Dir(store) != storesDir {
		return fmt.Errorf("upstream object store outside managed stores directory")
	}
	if _, err := git.RunBare(ctx, gateDir, "check-ref-format", "refs/runs/"+run.ID+"/head"); err != nil {
		return fmt.Errorf("invalid run retention namespace")
	}
	objects := []string{run.HeadSHA}
	if !nullObjectID(run.BaseSHA) {
		objects = append(objects, run.BaseSHA)
	}
	for _, oid := range extraObjects {
		if oid != "" {
			objects = append(objects, oid)
		}
	}
	for _, value := range []*string{run.SubmittedHeadSHA, run.ReviewApprovedHeadSHA, run.LastPushedSHA} {
		if value != nil && *value != "" {
			objects = append(objects, *value)
		}
	}
	lock, err := filelock.AcquireContext(ctx, store+".lock")
	if err != nil {
		return err
	}
	defer lock.Release()

	return importStoreObjects(ctx, store, gateDir, objects, run.ID)
}

// A new branch has no old tip; Git reports a null object ID rather than a
// commit to retain. This sentinel is valid only for the previous/base tip.
func nullObjectID(oid string) bool {
	return (len(oid) == 40 || len(oid) == 64) && strings.Trim(oid, "0") == ""
}
