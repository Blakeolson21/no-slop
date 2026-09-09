package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/Blakeolson21/no-slop/internal/db"
	"github.com/Blakeolson21/no-slop/internal/ipc"
	"github.com/Blakeolson21/no-slop/internal/paths"
	"github.com/spf13/cobra"
	toon "github.com/toon-format/toon-go"
)

func newResponseKey() string {
	return rand.Text()
}

func responseReceiptCommand(runID, key string) string {
	return fmt.Sprintf("no-slop axi respond --receipt --run %s --idempotency-key %s", runID, key)
}

func sendResponse(ctx context.Context, client *ipc.Client, params ipc.RespondParams) (*ipc.RespondResult, error) {
	var result ipc.RespondResult
	if err := client.CallWithContext(ctx, ipc.MethodRespond, &params, &result, 0); err != nil {
		var rpcErr *ipc.RPCError
		if errors.As(err, &rpcErr) {
			return nil, err
		}
		return nil, fmt.Errorf("%w; the ruling may have been accepted. Check acceptance with `%s`; retry the original ruling only with the same --run, --step and --idempotency-key", err, responseReceiptCommand(params.RunID, params.IdempotencyKey))
	}
	if !result.OK {
		return nil, fmt.Errorf("daemon rejected the response")
	}
	if result.IdempotencyKey != params.IdempotencyKey || result.RunID != params.RunID || result.Step != params.Step {
		return nil, fmt.Errorf("daemon accepted the ruling without a matching receipt; it may be an older daemon without retry protection. Do not resend this ruling")
	}
	return &result, nil
}

func runAxiReceipt(cmd *cobra.Command, args respondArgs) (string, error) {
	if err := ipc.ValidateResponseKey(args.idempotencyKey); err != nil {
		return "", emitError(cmd, 2, err.Error())
	}
	if args.runID == "" || args.idempotencyKey == "" || args.action != "" || args.autoYes || args.noWait || args.step != "" || args.findings != "" || args.instructions != "" || args.addFinding != "" {
		return "", emitError(cmd, 2, "--receipt requires --run and --idempotency-key, without an action, step, or fix options")
	}
	p, err := paths.New()
	if err != nil {
		return "", emitError(cmd, 1, fmt.Sprintf("resolve paths: %v", err))
	}
	database, err := db.OpenReadOnlyNoCreate(p.DB())
	if err != nil {
		return "", emitError(cmd, 1, fmt.Sprintf("open response receipts read-only: %v", err))
	}
	defer database.Close()
	receipt, err := database.GetResponseReceipt(args.runID, args.idempotencyKey)
	if err != nil {
		return "", emitError(cmd, 1, fmt.Sprintf("read response receipt: %v", err))
	}
	result := &ipc.RespondResult{RunID: args.runID, IdempotencyKey: args.idempotencyKey}
	fingerprint := strings.Join([]string{args.runID, args.idempotencyKey, "missing"}, "|")
	if receipt != nil {
		result.OK = true
		result.Step = receipt.Step
		result.Round = receipt.Round
		fingerprint = strings.Join([]string{receipt.RunID, receipt.IdempotencyKey, string(receipt.Step), fmt.Sprint(receipt.Round)}, "|")
	}
	renderResponseReceipt(cmd, result)
	return fingerprint, nil
}

func renderResponseReceipt(cmd *cobra.Command, result *ipc.RespondResult) {
	fields := []toon.Field{
		{Key: "accepted", Value: result.OK},
		{Key: "run", Value: result.RunID},
		{Key: "idempotency_key", Value: result.IdempotencyKey},
	}
	if result.OK {
		fields = append(fields, toon.Field{Key: "step", Value: string(result.Step)}, toon.Field{Key: "round", Value: result.Round}, toon.Field{Key: "replayed", Value: result.Replayed}, toon.Field{Key: "note", Value: "Acceptance is recorded; execution may still be pending or interrupted."})
	} else {
		fields = append(fields, toon.Field{Key: "note", Value: "No acceptance recorded as of this lookup. An in-flight request may still arrive; retry the original ruling only with the same idempotency key."})
	}
	fields = append(fields, toon.Field{Key: "help", Value: []string{fmt.Sprintf("Run `no-slop axi status --run %s` to inspect execution", result.RunID)}})
	emitDoc(cmd, fields...)
}
