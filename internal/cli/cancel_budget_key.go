package cli

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// cancelBudgetKeyFile is the conventional location of the adjudicator public
// key. Its bytes are never trusted on the strength of this path alone: the
// store root is caller-selected and lane-writable, so a key found here must
// still be authorized by operator-owned authority below.
const cancelBudgetKeyFile = "cancel-budget-public-key"

// cancelBudgetPolicyFile is the operator policy installed beside the running
// no-slop executable by the fleet deployment, not inside any store. It pins the
// adjudicator key by fingerprint.
const cancelBudgetPolicyFile = "cancel-budget-policy.json"

// cancelBudgetPolicy is the installed operator policy. Only the fingerprint is
// authority; unknown fields are ignored so the deployment can carry provenance
// notes alongside it.
type cancelBudgetPolicy struct {
	PublicKeySHA256 string `json:"public_key_sha256"`
}

// cancelBudgetAuthority describes why a public key was accepted, so the
// operator can see from the command output which authority admitted it.
type cancelBudgetAuthority struct {
	Key    ed25519.PublicKey
	Source string // the key file that supplied the bytes
	Bound  string // "operator-owned-path" or "installed-policy:<path>"
}

// resolveCancelBudgetKey loads the adjudicator public key and refuses unless its
// identity is bound to authority the lane under review cannot forge.
//
// Two bindings are accepted, in order:
//
//  1. Operator-owned path. The key file and its directory are owned by root (or
//     by some uid other than the caller's) and are not group- or world-writable,
//     so the invoking user can neither edit the file nor replace it by unlinking
//     it. This is the configuration the deployment worksheet provisions.
//
//  2. Installed policy fingerprint. The policy file sits beside the running
//     executable - operator-deployed bytes restored from main by the dispatcher
//     pin timer, outside any caller-selected store - and pins the key's
//     SHA-256. The key bytes must hash to it exactly.
//
// Anything else is refused. Previously the command trusted whatever bytes
// occupied <NS_HOME>/cancel-budget-public-key, so a lane with ordinary store
// write access could install its own key and sign its own waiver.
func resolveCancelBudgetKey(storeRoot string) (*cancelBudgetAuthority, error) {
	path := filepath.Join(storeRoot, cancelBudgetKeyFile)
	keyText, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read independent adjudicator key: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyText)))
	if err != nil {
		return nil, fmt.Errorf("cancel budget: adjudicator key is not base64: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("cancel budget: adjudicator key must be %d raw ed25519 bytes, got %d", ed25519.PublicKeySize, len(raw))
	}

	if pathErr := requireOperatorOwned(path); pathErr == nil {
		return &cancelBudgetAuthority{Key: ed25519.PublicKey(raw), Source: path, Bound: "operator-owned-path"}, nil
	} else {
		policyPath, policyErr := cancelBudgetPolicyPath(storeRoot)
		if policyErr != nil {
			return nil, fmt.Errorf("cancel budget: adjudicator key at %s is not operator-provisioned (%v) and no installed policy binds it: %w", path, pathErr, policyErr)
		}
		if err := bindKeyToPolicy(policyPath, raw); err != nil {
			return nil, fmt.Errorf("cancel budget: adjudicator key at %s is not operator-provisioned (%v): %w", path, pathErr, err)
		}
		return &cancelBudgetAuthority{Key: ed25519.PublicKey(raw), Source: path, Bound: "installed-policy:" + policyPath}, nil
	}
}

// cancelBudgetPolicyPath locates the installed operator policy beside the
// running executable and refuses a policy that lives inside the caller-selected
// store, which would put the trust root back where the lane can write it.
func cancelBudgetPolicyPath(storeRoot string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate installed executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	policy := filepath.Join(filepath.Dir(exe), cancelBudgetPolicyFile)
	if within, err := pathWithin(storeRoot, policy); err != nil {
		return "", err
	} else if within {
		return "", fmt.Errorf("installed policy %s is inside the caller-selected store %s", policy, storeRoot)
	}
	if _, err := os.Stat(policy); err != nil {
		return "", fmt.Errorf("no installed operator policy at %s: %w", policy, err)
	}
	return policy, nil
}

func bindKeyToPolicy(policyPath string, key []byte) error {
	info, err := os.Stat(policyPath)
	if err != nil {
		return fmt.Errorf("read installed operator policy: %w", err)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("installed operator policy %s is group- or world-writable", policyPath)
	}
	data, err := os.ReadFile(policyPath)
	if err != nil {
		return fmt.Errorf("read installed operator policy: %w", err)
	}
	var policy cancelBudgetPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return fmt.Errorf("parse installed operator policy %s: %w", policyPath, err)
	}
	pinned := strings.ToLower(strings.TrimSpace(policy.PublicKeySHA256))
	if pinned == "" {
		return fmt.Errorf("installed operator policy %s pins no public_key_sha256", policyPath)
	}
	digest := sha256.Sum256(key)
	if hex.EncodeToString(digest[:]) != pinned {
		return fmt.Errorf("adjudicator key does not match the fingerprint pinned by %s", policyPath)
	}
	return nil
}

// requireOperatorOwned reports whether path is material the invoking user
// cannot rewrite: neither the file nor the directory holding it may be owned by
// the caller or writable by its group or by others. Ownership is the load-
// bearing half - a file the caller owns can always be chmod'd back open, and a
// directory the caller owns lets the file be replaced wholesale.
func requireOperatorOwned(path string) error {
	if err := ownedByOperator(path); err != nil {
		return err
	}
	return ownedByOperator(filepath.Dir(path))
}

func ownedByOperator(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is group- or world-writable", path)
	}
	uid, ok := fileOwnerUID(info)
	if !ok {
		return fmt.Errorf("%s has no readable ownership", path)
	}
	caller := os.Geteuid()
	if caller != 0 && uid == caller {
		return fmt.Errorf("%s is owned by the invoking user", path)
	}
	return nil
}

// pathWithin reports whether candidate is root or lives under it, comparing
// cleaned absolute paths so ".." cannot walk a policy back into the store.
func pathWithin(root, candidate string) (bool, error) {
	if strings.TrimSpace(root) == "" {
		return false, nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(absRoot, absCandidate)
	if err != nil {
		return false, nil
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}
