package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// launchReceipt carries the daemon's returned ID, never a neighboring run
// discovered by time or branch. The push hook writes before returning; AXI
// publishes the same identity before it begins driving that exact run.
type launchReceipt struct {
	RunID         string `json:"run_id"`
	Branch        string `json:"branch"`
	SubmittedHead string `json:"submitted_head"`
	StoreRoot     string `json:"store_root"`
	StoreHost     string `json:"store_host"`
	Repository    string `json:"repository,omitempty"`
}

func writeLaunchReceipt(path string, receipt launchReceipt) error {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) || receipt.RunID == "" {
		return fmt.Errorf("launch receipt requires absolute path and captured run ID")
	}
	host, err := os.Hostname()
	if err != nil {
		return err
	}
	receipt.StoreHost = host
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".launch-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func capturedPushRun(path, branch, head string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", true, err
	}
	var row launchReceipt
	if err = json.Unmarshal(data, &row); err != nil {
		return "", true, fmt.Errorf("invalid captured launch receipt: %w", err)
	}
	if strings.TrimSpace(row.RunID) == "" || row.Branch != branch || row.SubmittedHead != head {
		return "", true, fmt.Errorf("captured launch identity mismatch: run=%q branch=%q head=%q", row.RunID, row.Branch, row.SubmittedHead)
	}
	return row.RunID, true, nil
}
