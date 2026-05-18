package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/transport/api"
)

// jsonEnvelope is the stable --json output shape emitted by all five read-only
// subcommands when the --json flag or CLAWCTL_OUTPUT=json env var is set.
type jsonEnvelope struct {
	Command string          `json:"command"`
	Ok      bool            `json:"ok"`
	Data    json.RawMessage `json:"data"`
	Error   *jsonError      `json:"error"`
}

type jsonError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	TraceID string `json:"trace_id,omitempty"`
}

// writeJSONOK emits a success envelope: ok=true, data=<data>, error=null.
func writeJSONOK(w io.Writer, command string, data json.RawMessage) error {
	if data == nil {
		data = json.RawMessage("null")
	}
	env := jsonEnvelope{
		Command: command,
		Ok:      true,
		Data:    data,
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(env)
}

// writeJSONErr emits an error envelope: ok=false, data=null, error=<err>.
func writeJSONErr(w io.Writer, command string, exitCode int, message, traceID string) error {
	env := jsonEnvelope{
		Command: command,
		Ok:      false,
		Data:    json.RawMessage("null"),
		Error: &jsonError{
			Code:    exitCodeToString(exitCode),
			Message: message,
			TraceID: traceID,
		},
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(env)
}

func exitCodeToString(code int) string {
	switch code {
	case 1:
		return "not_found"
	case 2:
		return "usage_error"
	case 6:
		return "dns_failure"
	case 7:
		return "conn_refused"
	case 22:
		return "http_error"
	case 28:
		return "timeout"
	default:
		return "error"
	}
}

// apiErrorDetails maps api transport errors to (exitCode, message) without
// writing to any writer. Mirrors the switch statements in reportXxxError.
func apiErrorDetails(cfg config.Config, err error) (exitCode int, message string) {
	var httpErr *api.HTTPError
	var dnsErr *api.DNSError
	var refErr *api.ConnRefusedError
	var toErr *api.TimeoutError
	switch {
	case errors.As(err, &httpErr):
		return 22, fmt.Sprintf("gateway error: HTTP %d", httpErr.StatusCode)
	case errors.As(err, &dnsErr):
		return 6, fmt.Sprintf("DNS resolution failed for %s", cfg.Host)
	case errors.As(err, &refErr):
		return 7, fmt.Sprintf("connection refused: %s", cfg.Host)
	case errors.As(err, &toErr):
		return 28, fmt.Sprintf("timeout (%ds) calling %s", int(cfg.Timeout.Seconds()), cfg.Host)
	default:
		return api.ExitCode(err), err.Error()
	}
}

// toRawJSON returns body as a json.RawMessage if it is valid JSON, or null.
func toRawJSON(body []byte) json.RawMessage {
	if json.Valid(body) {
		return json.RawMessage(body)
	}
	return json.RawMessage("null")
}
