package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// opencodeErrorServer serves the minimal session lifecycle with a
// caller-supplied POST /session/{id}/message body, and reports how many
// message requests it answered. The SSE stream carries no text, which is
// what real opencode emits for a turn that failed before the model replied.
func opencodeErrorServer(t *testing.T, bodies ...string) (*httptest.Server, *int) {
	t.Helper()
	sent := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session" && r.Method == http.MethodPost:
			fmt.Fprint(w, `{"id":"s1"}`)

		case r.URL.Path == "/global/event" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "data: {\"payload\":{\"type\":\"session.idle\"}}\n\n")

		case r.URL.Path == "/session/s1/message" && r.Method == http.MethodPost:
			body := bodies[len(bodies)-1]
			if sent < len(bodies) {
				body = bodies[sent]
			}
			sent++
			fmt.Fprint(w, body)

		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(server.Close)
	return server, &sent
}

func runOpencodeAgainst(t *testing.T, server *httptest.Server) (*Result, error) {
	t.Helper()
	a := &opencodeAgent{
		bin:    "opencode",
		server: &managedServer{port: mustParsePort(server.URL)},
	}
	return a.Run(context.Background(), RunOpts{
		Prompt:     "review this code",
		CWD:        t.TempDir(),
		JSONSchema: json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"]}`),
	})
}

// providerRejectedBody is the response real opencode returns when the
// provider rejects the forced tool_choice that a json_schema request
// carries: HTTP 200, no parts, zero tokens, and the whole cause nested
// under info.error.data.
const providerRejectedBody = `{"info":{"id":"msg1","role":"assistant","tokens":{"input":0,"output":0},` +
	`"error":{"name":"APIError","data":{"message":"Error from provider (Console Go): Upstream request failed: ` +
	`[invalid_request_error] Thinking mode does not support this tool_choice","statusCode":400,"isRetryable":false}}},"parts":[]}`

// TestOpencodeAgent_FailedTurnSurfacesProviderErrorInsteadOfEmptyOutput is
// the regression for a review step that failed in seconds with zero tokens
// and reported only "opencode returned no text output". opencode reports a
// failed turn on info.error with HTTP 200, so every variant other than
// StructuredOutputError used to be dropped and the run fell through to the
// empty-text fallback, hiding an actionable provider rejection.
func TestOpencodeAgent_FailedTurnSurfacesProviderErrorInsteadOfEmptyOutput(t *testing.T) {
	server, sent := opencodeErrorServer(t, providerRejectedBody)

	result, err := runOpencodeAgainst(t, server)
	if err == nil {
		t.Fatalf("expected error, got result %+v", result)
	}
	if result != nil {
		t.Fatalf("expected nil result on error, got %+v", result)
	}
	msg := err.Error()
	if strings.Contains(msg, "no text output") {
		t.Errorf("failed turn must not be reported as empty output, got %q", msg)
	}
	for _, want := range []string{"APIError", "400", "Thinking mode does not support this tool_choice"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected error to carry %q, got %q", want, msg)
		}
	}
	// A provider that rejected the request as invalid will reject it again,
	// so the step must fail on the first attempt rather than spending the
	// retry budget.
	if *sent != 1 {
		t.Errorf("expected 1 message request for a non-retryable error, got %d", *sent)
	}
}

// TestOpencodeAgent_StructuredOutputErrorReadsNestedErrorData pins the wire
// shape: opencode serializes every named error as {"name":..,"data":{..}},
// so reading message/retries from the top level left the user-facing error
// blank ("failed after 0 internal retries: ").
func TestOpencodeAgent_StructuredOutputErrorReadsNestedErrorData(t *testing.T) {
	server, _ := opencodeErrorServer(t, `{"info":{"id":"msg1","role":"assistant","error":{"name":"StructuredOutputError",`+
		`"data":{"message":"Model did not produce structured output","retries":2}}},"parts":[]}`)

	_, err := runOpencodeAgainst(t, server)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "2 internal retries") {
		t.Errorf("expected nested retry count in error, got %q", msg)
	}
	if !strings.Contains(msg, "Model did not produce structured output") {
		t.Errorf("expected nested message in error, got %q", msg)
	}
}

// Provider-reported failures are not replayed in a fresh session because the
// failed turn may already have performed tool side effects.
func TestOpencodeAgent_RetryableProviderErrorIsSurfacedWithoutFreshSessionReplay(t *testing.T) {
	server, sent := opencodeErrorServer(t,
		`{"info":{"id":"msg1","role":"assistant","error":{"name":"APIError","data":{"message":"Service Unavailable","statusCode":503,"isRetryable":true}}},"parts":[]}`,
	)

	_, err := runOpencodeAgainst(t, server)
	if err == nil || !strings.Contains(err.Error(), "Service Unavailable") {
		t.Fatalf("error = %v, want surfaced provider failure", err)
	}
	if *sent != 1 {
		t.Errorf("message requests = %d, want no replay", *sent)
	}
}
