package cli

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installedPolicyDir is the directory holding the running executable, which is
// where a deployed no-slop finds its operator policy. Tests write there so the
// binding they exercise is the real lookup, not a stub.
func installedPolicyDir(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

func writeInstalledPolicy(t *testing.T, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(installedPolicyDir(t), cancelBudgetPolicyFile)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(path) })
}

// writeStoreKey drops a base64 ed25519 public key where a lane with ordinary
// store write access can put one, and returns its fingerprint.
func writeStoreKey(t *testing.T, store string) (ed25519.PublicKey, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, cancelBudgetKeyFile), []byte(base64.StdEncoding.EncodeToString(pub)), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(pub)
	return pub, hex.EncodeToString(digest[:])
}

// A key the invoking lane can rewrite is not an authority. Without an operator
// binding the command must refuse rather than verify a waiver the lane signed
// for itself.
func TestCancelBudgetKeyRefusesAKeyTheLaneCanReplace(t *testing.T) {
	store := t.TempDir()
	writeStoreKey(t, store)
	authority, err := resolveCancelBudgetKey(store)
	if err == nil {
		t.Fatalf("a lane-writable key was accepted under %s", authority.Bound)
	}
	if !strings.Contains(err.Error(), "not operator-provisioned") {
		t.Fatalf("refusal did not name the missing authority: %v", err)
	}
}

// The installed operator policy is the binding. It must pin this exact key.
func TestCancelBudgetKeyBindsToTheInstalledPolicyFingerprint(t *testing.T) {
	store := t.TempDir()
	pub, fingerprint := writeStoreKey(t, store)

	other, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	otherDigest := sha256.Sum256(other)
	writeInstalledPolicy(t, `{"public_key_sha256":"`+hex.EncodeToString(otherDigest[:])+`"}`, 0o644)
	if _, err := resolveCancelBudgetKey(store); err == nil {
		t.Fatal("a key the installed policy does not pin was accepted")
	} else if !strings.Contains(err.Error(), "does not match the fingerprint") {
		t.Fatalf("refusal did not name the fingerprint mismatch: %v", err)
	}

	writeInstalledPolicy(t, `{"public_key_sha256":"`+fingerprint+`"}`, 0o644)
	authority, err := resolveCancelBudgetKey(store)
	if err != nil {
		t.Fatalf("the pinned operator key was refused: %v", err)
	}
	if !authority.Key.Equal(pub) {
		t.Fatal("a different key was returned than the one the policy pinned")
	}
	if !strings.HasPrefix(authority.Bound, "installed-policy:") {
		t.Fatalf("authority = %q", authority.Bound)
	}
}

// A policy the lane can rewrite is no better than an unbound key: a policy that
// pins nothing, or one anybody may write, must be refused.
func TestCancelBudgetKeyRefusesAWeakInstalledPolicy(t *testing.T) {
	store := t.TempDir()
	_, fingerprint := writeStoreKey(t, store)

	writeInstalledPolicy(t, `{"note":"no fingerprint here"}`, 0o644)
	if _, err := resolveCancelBudgetKey(store); err == nil || !strings.Contains(err.Error(), "pins no public_key_sha256") {
		t.Fatalf("a policy pinning nothing was accepted: %v", err)
	}

	writeInstalledPolicy(t, `{"public_key_sha256":"`+fingerprint+`"}`, 0o666)
	if _, err := resolveCancelBudgetKey(store); err == nil || !strings.Contains(err.Error(), "world-writable") {
		t.Fatalf("a world-writable policy was accepted: %v", err)
	}
}

// The store is caller-selected, so a policy discovered inside it would put the
// trust root straight back where the lane writes.
func TestCancelBudgetKeyRefusesAPolicyInsideTheStore(t *testing.T) {
	store := installedPolicyDir(t)
	_, fingerprint := writeStoreKey(t, store)
	writeInstalledPolicy(t, `{"public_key_sha256":"`+fingerprint+`"}`, 0o644)
	t.Cleanup(func() { os.Remove(filepath.Join(store, cancelBudgetKeyFile)) })

	if _, err := resolveCancelBudgetKey(store); err == nil || !strings.Contains(err.Error(), "inside the caller-selected store") {
		t.Fatalf("a policy inside the store was accepted: %v", err)
	}
}

// Malformed key material is refused before any signature check, so a truncated
// or non-base64 file cannot be mistaken for a short-but-valid key.
func TestCancelBudgetKeyRefusesMalformedKeyMaterial(t *testing.T) {
	for name, body := range map[string]string{
		"not base64":  "!!!!not-base64!!!!",
		"wrong size":  base64.StdEncoding.EncodeToString([]byte("too short")),
		"empty":       "",
		"has newline": base64.StdEncoding.EncodeToString(make([]byte, 16)),
	} {
		t.Run(name, func(t *testing.T) {
			store := t.TempDir()
			if err := os.WriteFile(filepath.Join(store, cancelBudgetKeyFile), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := resolveCancelBudgetKey(store); err == nil {
				t.Fatal("malformed key material accepted")
			}
		})
	}
}
