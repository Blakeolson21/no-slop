package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCapturedLaunchIdentityIsIndependentOfVisibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launch.json")
	row := launchReceipt{RunID: "captured-but-not-yet-visible", Branch: "codex/test", SubmittedHead: "abc", StoreRoot: "/isolated/store"}
	if err := writeLaunchReceipt(path, row); err != nil {
		t.Fatal(err)
	}
	id, captured, err := capturedPushRun(path, "codex/test", "abc")
	if err != nil || !captured || id != row.RunID {
		t.Fatalf("id=%q captured=%v error=%v", id, captured, err)
	}
}

func TestCapturedLaunchMismatchCannotBecomeLegacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launch.json")
	if err := writeLaunchReceipt(path, launchReceipt{RunID: "ours", Branch: "codex/other", SubmittedHead: "abc"}); err != nil {
		t.Fatal(err)
	}
	id, captured, err := capturedPushRun(path, "codex/test", "abc")
	if err == nil || !captured || id != "" {
		t.Fatalf("mismatch accepted: id=%q captured=%v error=%v", id, captured, err)
	}
	if err := os.WriteFile(path, []byte(`{"run_id":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, captured, err = capturedPushRun(path, "codex/test", "abc"); err == nil || !captured {
		t.Fatal("malformed capture treated as legacy")
	}
}

func TestLegacyLaunchHasNoCapture(t *testing.T) {
	_, captured, err := capturedPushRun(filepath.Join(t.TempDir(), "absent.json"), "codex/test", "abc")
	if err != nil || captured {
		t.Fatalf("legacy: captured=%v error=%v", captured, err)
	}
}
