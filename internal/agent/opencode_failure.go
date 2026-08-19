package agent

import (
	"errors"
	"fmt"
	"strings"
)

// opencodeMessageFailure is a turn that opencode completed with an error on
// the assistant message. It carries opencode's own retryability verdict so
// the retry loop can repeat a provider blip without ever repeating a request
// the provider rejected as invalid.
type opencodeMessageFailure struct {
	name       string
	message    string
	statusCode int
	retries    int
	structured bool
}

// classifyOpencodeTransportTransient keeps existing transport retries while
// refusing to replay a provider-reported completed turn in a fresh session.
func classifyOpencodeTransportTransient(err error) (string, bool) {
	var failure *opencodeMessageFailure
	if errors.As(err, &failure) {
		return "", false
	}
	return classifyTransient(err)
}

func newOpencodeMessageFailure(e *opencodeMessageError) error {
	if e == nil {
		return nil
	}
	return &opencodeMessageFailure{
		name:       e.Name,
		message:    e.message(),
		statusCode: e.statusCode(),
		retries:    e.retries(),
		structured: e.IsStructuredOutput(),
	}
}

func (e *opencodeMessageFailure) Error() string {
	// StructuredOutputError keeps its own wording: the actionable fact is
	// that opencode already spent its internal retries trying to make the
	// model call the StructuredOutput tool.
	if e.structured {
		return fmt.Sprintf("opencode structured output failed after %d internal retries: %s",
			e.retries, e.detail())
	}
	name := e.name
	if name == "" {
		name = "error"
	}
	if e.statusCode != 0 {
		return fmt.Sprintf("opencode %s (status %d): %s", name, e.statusCode, e.detail())
	}
	return fmt.Sprintf("opencode %s: %s", name, e.detail())
}

func (e *opencodeMessageFailure) detail() string {
	if msg := strings.TrimSpace(e.message); msg != "" {
		return msg
	}
	return "no detail reported"
}
