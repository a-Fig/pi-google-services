package services

import (
	"errors"
	"strings"
	"testing"
)

func TestSummarizeErr_Nil(t *testing.T) {
	if got := summarizeErr(nil); got != "" {
		t.Errorf("summarizeErr(nil) = %q, want empty string", got)
	}
}

func TestSummarizeErr_Short(t *testing.T) {
	err := errors.New("simple failure")
	if got := summarizeErr(err); got != "simple failure" {
		t.Errorf("summarizeErr = %q, want unchanged short message", got)
	}
}

func TestSummarizeErr_MultiLine(t *testing.T) {
	err := errors.New("googleapi: Error 400:\nInvalid request.\n\nDetails: bad thread id")
	got := summarizeErr(err)
	if strings.Contains(got, "\n") {
		t.Errorf("summarizeErr left a newline in %q", got)
	}
	want := "googleapi: Error 400: Invalid request. Details: bad thread id"
	if got != want {
		t.Errorf("summarizeErr(multiline) = %q, want %q", got, want)
	}
}

func TestSummarizeErr_CollapsesWhitespaceRuns(t *testing.T) {
	err := errors.New("a   b\t\tc\n\nd")
	if got, want := summarizeErr(err), "a b c d"; got != want {
		t.Errorf("summarizeErr = %q, want %q", got, want)
	}
}

func TestSummarizeErr_TruncatesLongMessage(t *testing.T) {
	long := strings.Repeat("word ", 100) // 500 chars, well over the cap
	err := errors.New(long)
	got := summarizeErr(err)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("summarizeErr(long) = %q, want a truncation marker", got)
	}
	// The cap is on runes, not bytes; check we didn't run far past it.
	runeLen := len([]rune(got)) - 1 // exclude the marker
	if runeLen != maxErrSummaryLen {
		t.Errorf("truncated length = %d runes, want %d", runeLen, maxErrSummaryLen)
	}
}

func TestSummarizeErr_TruncatesOnRuneBoundary(t *testing.T) {
	// Multi-byte runes (no spaces, so it stays one long "word") straddling the
	// cutoff must not be split mid-rune — []rune indexing already guarantees
	// this, but the test protects the invariant against a future switch back
	// to byte slicing.
	long := strings.Repeat("日", 400)
	err := errors.New(long)
	got := summarizeErr(err)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected truncation, got %q", got)
	}
	body := strings.TrimSuffix(got, "…")
	for _, r := range body {
		if r != '日' {
			t.Fatalf("truncation produced a corrupted rune: %q", got)
		}
	}
	if got := len([]rune(body)); got != maxErrSummaryLen {
		t.Errorf("truncated body = %d runes, want %d", got, maxErrSummaryLen)
	}
}

func TestRPCError_Fields(t *testing.T) {
	err := errors.New("googleapi: Error 404: Not Found\nfull response body here")
	rpcErr := rpcError("reply", err)

	if rpcErr.Code != -32603 {
		t.Errorf("Code = %d, want -32603", rpcErr.Code)
	}
	wantMsg := "Failed to reply: googleapi: Error 404: Not Found full response body here"
	if rpcErr.Message != wantMsg {
		t.Errorf("Message = %q, want %q", rpcErr.Message, wantMsg)
	}
	if rpcErr.Data != err.Error() {
		t.Errorf("Data = %v, want the untruncated error %q", rpcErr.Data, err.Error())
	}
}

func TestRPCError_MessageStaysShortForLongError(t *testing.T) {
	long := strings.Repeat("x", 5000)
	rpcErr := rpcError("do thing", errors.New(long))
	if len(rpcErr.Message) > len("Failed to do thing: ")+maxErrSummaryLen+len("…") {
		t.Errorf("Message too long: %d chars", len(rpcErr.Message))
	}
	if rpcErr.Data != long {
		t.Errorf("Data was altered, want the full untruncated error preserved")
	}
}
