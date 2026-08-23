package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Blakeolson21/no-slop/internal/buildinfo"
)

func TestDefaultStaysNoopWithFullTelemetryConfiguration(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	prevSink := defaultSink
	defaultSink = nil
	defer func() { defaultSink = prevSink }()
	prevHost, prevVersion, prevWebsiteID := buildinfo.TelemetryHost, buildinfo.Version, buildinfo.TelemetryWebsiteID
	defer func() {
		buildinfo.TelemetryHost, buildinfo.Version, buildinfo.TelemetryWebsiteID = prevHost, prevVersion, prevWebsiteID
	}()
	buildinfo.TelemetryHost = server.URL
	buildinfo.Version = "v1.2.3"
	buildinfo.TelemetryWebsiteID = "embedded-website"

	t.Setenv(telemetryEnv, "on")
	t.Setenv(legacyTelemetryEnv, "on")
	t.Setenv(umamiHostEnv, server.URL)
	t.Setenv(legacyUmamiHostEnv, server.URL)
	t.Setenv(umamiWebsiteIDEnv, "runtime-website")
	t.Setenv(legacyUmamiWebsiteIDEnv, "runtime-website")

	if websiteID, err := defaultWebsiteID(); err != nil || websiteID == "" {
		t.Fatalf("test setup did not resolve a configured collector: website ID %q, err %v", websiteID, err)
	}
	sink, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sink.(noopSink); !ok {
		t.Fatalf("Default() type = %T, want noopSink", sink)
	}
	if Enabled() {
		t.Fatal("Enabled() = true, want false")
	}
	Track("command", Fields{"command": "status"})
	Pageview("/tui", nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("remote collector received %d requests, want zero", got)
	}
}

func TestSetDefaultForTestingStillInjectsRecorder(t *testing.T) {
	prevSink := defaultSink
	defaultSink = nil
	defer func() { defaultSink = prevSink }()
	recorder := &recordingSink{}
	restore := SetDefaultForTesting(recorder)

	Track("command", nil)
	if recorder.tracked != 1 {
		t.Fatalf("recorded events = %d, want 1", recorder.tracked)
	}
	restore()
	sink, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sink.(noopSink); !ok {
		t.Fatalf("Default() after restore = %T, want noopSink", sink)
	}
}

type recordingSink struct{ tracked int }

func (s *recordingSink) Track(string, Fields)      { s.tracked++ }
func (*recordingSink) Pageview(string, Fields)     {}
func (*recordingSink) Close(context.Context) error { return nil }
