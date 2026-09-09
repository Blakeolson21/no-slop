package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/Blakeolson21/no-slop/internal/telemetry"
	"github.com/Blakeolson21/no-slop/internal/types"
)

func TestSendResponseUncertainTransportNamesReceiptCheck(t *testing.T) {
	srv := ipc.NewServer()
	received := make(chan struct{})
	release := make(chan struct{})
	srv.Handle(ipc.MethodRespond, func(context.Context, json.RawMessage) (interface{}, error) {
		close(received)
		<-release
		return &ipc.RespondResult{OK: true}, nil
	})
	client, _ := startDriveTestServer(t, srv)
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := sendResponse(ctx, client, ipc.RespondParams{RunID: "run-8", Step: types.StepReview, Action: types.ActionFix, IdempotencyKey: "ruling-8"})
		done <- err
	}()
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached server")
	}
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	for _, want := range []string{"may have been accepted", "no-slop axi respond --receipt --run run-8 --idempotency-key ruling-8", "same --run, --step and --idempotency-key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestSendResponseRejectsLegacyAcknowledgement(t *testing.T) {
	srv := ipc.NewServer()
	srv.Handle(ipc.MethodRespond, func(context.Context, json.RawMessage) (interface{}, error) { return &ipc.RespondResult{OK: true}, nil })
	client, _ := startDriveTestServer(t, srv)
	_, err := sendResponse(context.Background(), client, ipc.RespondParams{RunID: "run-8", Step: types.StepReview, Action: types.ActionFix, IdempotencyKey: "ruling-8"})
	if err == nil || !strings.Contains(err.Error(), "Do not resend") {
		t.Fatalf("legacy acknowledgement = %v", err)
	}
}

func TestAxiReceiptReadsAcceptanceWithoutDaemonOrWorktree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("NS_HOME", home)
	p := paths.WithRoot(home)
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := database.InsertRepoWithID("repo-1", "/not/required/for/receipt", "origin", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := database.InsertRun(repo.ID, "feature", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.InsertResponseReceipt(db.ResponseReceipt{RunID: run.ID, IdempotencyKey: "offline-ruling", Step: types.StepReview, Round: 4, RequestHash: "digest"}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), []byte("agent: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	beforeEntries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	beforeDB, err := os.Stat(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	beforeDBBytes, err := os.ReadFile(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := os.Stat(p.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	beforeConfigBytes, err := os.ReadFile(p.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	recorder := &telemetryRecorder{}
	restoreTelemetry := telemetry.SetDefaultForTesting(recorder)
	defer restoreTelemetry()
	t.Chdir(t.TempDir())
	out, err := executeCmd("axi", "respond", "--receipt", "--run", run.ID, "--idempotency-key", "offline-ruling")
	if err != nil {
		t.Fatalf("offline receipt: %v\n%s", err, out)
	}
	for _, want := range []string{"accepted: true", "round: 4", "step: review", "idempotency_key: offline-ruling"} {
		if !strings.Contains(out, want) {
			t.Errorf("receipt missing %q: %s", want, out)
		}
	}
	if _, err := os.Stat(p.Socket()); !os.IsNotExist(err) {
		t.Fatalf("receipt lookup started a daemon: %v", err)
	}
	afterEntries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	dbBase := filepath.Base(p.DB())
	if applicationEntryNames(beforeEntries, dbBase) != applicationEntryNames(afterEntries, dbBase) {
		t.Fatalf("receipt lookup changed application resources: before=%v after=%v", applicationEntryNames(beforeEntries, dbBase), applicationEntryNames(afterEntries, dbBase))
	}
	afterDB, err := os.Stat(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	if beforeDB.Size() != afterDB.Size() || !beforeDB.ModTime().Equal(afterDB.ModTime()) {
		t.Fatalf("receipt lookup modified database: before=%v after=%v", beforeDB, afterDB)
	}
	afterDBBytes, err := os.ReadFile(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeDBBytes, afterDBBytes) {
		t.Fatal("receipt lookup changed database contents or schema")
	}
	afterConfig, err := os.Stat(p.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	afterConfigBytes, err := os.ReadFile(p.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeConfigBytes, afterConfigBytes) || !beforeConfig.ModTime().Equal(afterConfig.ModTime()) {
		t.Fatal("receipt lookup changed global configuration")
	}
	if recorder.count("command") != 0 || recorder.count("pageview") != 0 {
		t.Fatal("receipt lookup emitted telemetry")
	}
	for _, telemetryPath := range []string{p.TelemetryGateFile(), p.TelemetryGateFile() + ".lock"} {
		if _, err := os.Stat(telemetryPath); !os.IsNotExist(err) {
			t.Fatalf("receipt lookup created telemetry state %q: %v", telemetryPath, err)
		}
	}
	out, err = executeCmd("axi", "respond", "--receipt", "--run", run.ID, "--idempotency-key", "unseen")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "accepted: false") || !strings.Contains(out, "in-flight request") {
		t.Fatalf("missing receipt should preserve uncertainty: %s", out)
	}
}

func applicationEntryNames(entries []os.DirEntry, dbBase string) string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == dbBase+"-wal" || entry.Name() == dbBase+"-shm" {
			continue
		}
		names = append(names, entry.Name())
	}
	return strings.Join(names, ",")
}

func serveReplayDaemon(t *testing.T, p *paths.Paths, current func() *ipc.RunInfo, transition <-chan struct{}) *atomic.Int32 {
	t.Helper()
	srv := ipc.NewServer()
	var subscriptions atomic.Int32
	srv.Handle(ipc.MethodHealth, func(context.Context, json.RawMessage) (interface{}, error) {
		return &ipc.HealthResult{Status: "ok"}, nil
	})
	srv.Handle(ipc.MethodGetRun, func(context.Context, json.RawMessage) (interface{}, error) {
		return &ipc.GetRunResult{Run: current()}, nil
	})
	srv.Handle(ipc.MethodRespond, func(_ context.Context, raw json.RawMessage) (interface{}, error) {
		var params ipc.RespondParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		return &ipc.RespondResult{OK: true, RunID: params.RunID, Step: params.Step, Round: 1, IdempotencyKey: params.IdempotencyKey, Replayed: true}, nil
	})
	srv.HandleStream(ipc.MethodSubscribe, func(ctx context.Context, _ json.RawMessage) (ipc.StreamFunc, error) {
		subscriptions.Add(1)
		return func(send func(interface{}) error) error {
			select {
			case <-ctx.Done():
				return nil
			case <-transition:
				if err := send(ipc.Event{Type: ipc.EventRunUpdated, RunID: current().ID}); err != nil {
					return err
				}
				<-ctx.Done()
				return nil
			}
		}, nil
	})
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(p.Socket()) }()
	t.Cleanup(func() {
		srv.Close()
		select {
		case <-errCh:
		case <-time.After(time.Second):
			t.Error("replay daemon did not stop")
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		client, err := ipc.Dial(p.Socket())
		if err == nil {
			client.Close()
			return &subscriptions
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("replay daemon did not start")
	return nil
}

func TestAxiSynchronousReplayObservesBlockedFollowOnWork(t *testing.T) {
	_, p, database, repo := setupAxiQueryRepo(t)
	run, err := database.InsertRun(repo.ID, "feature", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	var stage atomic.Int32
	transition := make(chan struct{})
	current := func() *ipc.RunInfo {
		testStatus := types.StepStatusRunning
		if stage.Load() == 1 {
			testStatus = types.StepStatusAwaitingApproval
		}
		return &ipc.RunInfo{ID: run.ID, Status: types.RunRunning, Steps: []ipc.StepResultInfo{
			{StepName: types.StepReview, Status: types.StepStatusCompleted, RoundCount: 1},
			{StepName: types.StepTest, Status: testStatus, RoundCount: 1},
		}}
	}
	subscriptions := serveReplayDaemon(t, p, current, transition)
	cmd := newAxiRespondCmd()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd.SetContext(ctx)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	done := make(chan error, 1)
	go func() {
		done <- runAxiRespond(cmd, respondArgs{runID: run.ID, step: "review", action: "approve", idempotencyKey: "replayed-ruling"})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for subscriptions.Load() < 2 && time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("synchronous replay returned before follow-on work advanced: %v\n%s", err, output.String())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	if subscriptions.Load() < 2 {
		t.Fatal("synchronous replay did not resume event-driven observation")
	}
	stage.Store(1)
	close(transition)
	if err := <-done; err != nil {
		t.Fatalf("synchronous replay: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "gate:") || !strings.Contains(output.String(), "step: test") {
		t.Fatalf("replay did not return the follow-on gate:\n%s", output.String())
	}
}

func TestAxiReplayReturnsNewerParkedGate(t *testing.T) {
	_, p, database, repo := setupAxiQueryRepo(t)
	run, err := database.InsertRun(repo.ID, "feature", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	current := func() *ipc.RunInfo {
		return &ipc.RunInfo{ID: run.ID, Status: types.RunRunning, Steps: []ipc.StepResultInfo{{StepName: types.StepReview, Status: types.StepStatusAwaitingApproval, RoundCount: 2}}}
	}
	serveReplayDaemon(t, p, current, nil)
	cmd := newAxiRespondCmd()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd.SetContext(ctx)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	if err := runAxiRespond(cmd, respondArgs{runID: run.ID, step: "review", action: "approve", idempotencyKey: "older-ruling"}); err != nil {
		t.Fatalf("replay at newer gate: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "gate:") || !strings.Contains(output.String(), "step: review") {
		t.Fatalf("replay did not return the newer parked gate:\n%s", output.String())
	}
}
