package services

import (
	"strings"

	"github.com/sombi/pi-google-services/internal/mcp"
)

// maxErrSummaryLen bounds how much of an error's text lands in the MCP
// Message field. googleapi errors can embed a raw HTTP response body running
// to several KB; Data keeps the whole thing, but Message needs a cap since
// that is the field most MCP clients actually surface to the model (see
// pi-vi issue #102 — a Gmail 4xx was reduced to "Failed to reply" because the
// client showed Message and dropped Data entirely).
const maxErrSummaryLen = 300

// summarizeErr collapses err's text to a single line — a newline or any other
// run of whitespace becomes one space — and truncates it to roughly
// maxErrSummaryLen runes, cutting on a rune boundary and marking the cut with
// "…". A nil err summarizes to "".
func summarizeErr(err error) string {
	if err == nil {
		return ""
	}
	// strings.Fields splits on runs of whitespace (including newlines) and
	// drops empty fields, so Join gives us the collapsed one-liner directly.
	collapsed := strings.Join(strings.Fields(err.Error()), " ")

	runes := []rune(collapsed)
	if len(runes) <= maxErrSummaryLen {
		return collapsed
	}
	return string(runes[:maxErrSummaryLen]) + "…"
}

// rpcError builds the -32603 (internal error) RPCError a service handler
// returns when an API call fails. Message carries "Failed to <verb>:
// <summary>" so a client that hides Data still shows the caller enough to
// diagnose the failure; Data keeps the complete, untruncated err.Error().
func rpcError(verb string, err error) *mcp.RPCError {
	return &mcp.RPCError{
		Code:    -32603,
		Message: "Failed to " + verb + ": " + summarizeErr(err),
		Data:    err.Error(),
	}
}
