package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

func TestReplySubject(t *testing.T) {
	tests := []struct {
		name   string
		thread string
		want   string
	}{
		{"plain subject gets a prefix", "Lunch on Friday", "Re: Lunch on Friday"},
		{"existing prefix is not stacked", "Re: Lunch on Friday", "Re: Lunch on Friday"},
		{"prefix match is case insensitive", "RE: Lunch", "RE: Lunch"},
		{"spaced prefix is recognized", "Re : Lunch", "Re : Lunch"},
		{"surrounding space is trimmed", "  Lunch  ", "Re: Lunch"},
		{"empty stays empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := replySubject(tt.thread); got != tt.want {
				t.Errorf("replySubject(%q) = %q, want %q", tt.thread, got, tt.want)
			}
		})
	}
}

func TestReferencesChain(t *testing.T) {
	tests := []struct {
		name      string
		existing  string
		messageID string
		want      string
	}{
		{"first reply starts the chain", "", "<a@mail>", "<a@mail>"},
		{"later reply appends", "<a@mail>", "<b@mail>", "<a@mail> <b@mail>"},
		{"already present is not repeated", "<a@mail> <b@mail>", "<b@mail>", "<a@mail> <b@mail>"},
		{"missing message id keeps the chain", "<a@mail>", "", "<a@mail>"},
		{"nothing at all stays empty", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := referencesChain(tt.existing, tt.messageID); got != tt.want {
				t.Errorf("referencesChain(%q, %q) = %q, want %q", tt.existing, tt.messageID, got, tt.want)
			}
		})
	}
}

func TestReplyHeaders(t *testing.T) {
	got := replyHeaders("<a@mail>", "<a@mail>")
	if !strings.Contains(got, "In-Reply-To: <a@mail>\r\n") {
		t.Errorf("missing In-Reply-To, got %q", got)
	}
	if !strings.Contains(got, "References: <a@mail>\r\n") {
		t.Errorf("missing References, got %q", got)
	}
	if replyHeaders("", "") != "" {
		t.Errorf("expected no headers when there is nothing to reference")
	}
}

func header(name, value string) *gmail.MessagePartHeader {
	return &gmail.MessagePartHeader{Name: name, Value: value}
}

func TestExtractReplyTarget(t *testing.T) {
	msg := &gmail.Message{Payload: &gmail.MessagePart{Headers: []*gmail.MessagePartHeader{
		// Senders spell this header both ways; canonicalization has to catch both.
		header("Message-ID", " <abc@mail> "),
		header("references", "<x@mail>"),
		header("Subject", "Lunch"),
		header("From", "sender@example.com"),
		header("Reply-To", "desk@example.com"),
	}}}

	got := extractReplyTarget(msg)
	if got.MessageID != "<abc@mail>" {
		t.Errorf("MessageID = %q, want <abc@mail>", got.MessageID)
	}
	if got.References != "<x@mail>" {
		t.Errorf("References = %q, want <x@mail>", got.References)
	}
	if got.Subject != "Lunch" {
		t.Errorf("Subject = %q, want Lunch", got.Subject)
	}
	if got.ReplyTo != "desk@example.com" {
		t.Errorf("ReplyTo = %q, want desk@example.com", got.ReplyTo)
	}
}

func TestExtractReplyTargetFallsBackToFrom(t *testing.T) {
	msg := &gmail.Message{Payload: &gmail.MessagePart{Headers: []*gmail.MessagePartHeader{
		header("From", "sender@example.com"),
	}}}
	if got := extractReplyTarget(msg); got.ReplyTo != "sender@example.com" {
		t.Errorf("ReplyTo = %q, want the From address", got.ReplyTo)
	}
}

func TestExtractReplyTargetHandlesEmptyMessage(t *testing.T) {
	if got := extractReplyTarget(nil); got == nil {
		t.Fatal("expected a zero target, not nil")
	}
	if got := extractReplyTarget(&gmail.Message{}); got.MessageID != "" {
		t.Errorf("expected empty target for a message with no payload")
	}
}

func decodeRaw(t *testing.T, msg *gmail.Message) string {
	t.Helper()
	decoded, err := base64.URLEncoding.DecodeString(msg.Raw)
	if err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	return string(decoded)
}

func TestBuildMessageCarriesReplyHeaders(t *testing.T) {
	headers := replyHeaders("<abc@mail>", "<x@mail> <abc@mail>")

	plain := decodeRaw(t, buildMessage("a@b.com", "Re: Lunch", "sure", nil, headers))
	if !strings.Contains(plain, "In-Reply-To: <abc@mail>") {
		t.Errorf("plain reply missing In-Reply-To:\n%s", plain)
	}
	if !strings.Contains(plain, "References: <x@mail> <abc@mail>") {
		t.Errorf("plain reply missing References:\n%s", plain)
	}
	// The headers must land in the header block, not inside the body.
	if strings.Index(plain, "In-Reply-To:") > strings.Index(plain, "\r\n\r\n") {
		t.Errorf("threading headers leaked into the body:\n%s", plain)
	}

	withAttachment := decodeRaw(t, buildMessage("a@b.com", "Re: Lunch", "sure",
		[]Attachment{{Filename: "menu.txt", Data: []byte("soup")}}, headers))
	if !strings.Contains(withAttachment, "In-Reply-To: <abc@mail>") {
		t.Errorf("multipart reply missing In-Reply-To:\n%s", withAttachment)
	}
	if !strings.Contains(withAttachment, "multipart/mixed") {
		t.Errorf("multipart reply lost its content type")
	}
}

func TestCreateMessageHasNoReplyHeaders(t *testing.T) {
	plain := decodeRaw(t, createMessage("a@b.com", "Lunch", "hi", nil))
	if strings.Contains(plain, "In-Reply-To") || strings.Contains(plain, "References") {
		t.Errorf("a fresh email must not claim to be a reply:\n%s", plain)
	}
}

// fakeGmailUsersService points a real *gmail.UsersService at a local test
// server instead of gmail.googleapis.com, so threadReplyTarget's HTTP-level
// behavior (a 404 from Threads.Get triggering the Messages.Get fallback) can
// be exercised without a live Google API call.
func fakeGmailUsersService(t *testing.T, handler http.HandlerFunc) *gmail.UsersService {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	svc, err := gmail.NewService(context.Background(),
		option.WithEndpoint(ts.URL),
		option.WithoutAuthentication(),
		option.WithHTTPClient(ts.Client()))
	if err != nil {
		t.Fatalf("new gmail service: %v", err)
	}
	return svc.Users
}

// These run inside the test server's handler goroutine, where t.Fatal is not
// allowed (FailNow must be called from the test goroutine), so they report
// with t.Errorf and let the request finish.
func writeGoogleAPIError(t *testing.T, w http.ResponseWriter, code int, message string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{"code": code, "message": message},
	}); err != nil {
		t.Errorf("encode error body: %v", err)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func threadWithMessage(id string) *gmail.Thread {
	return &gmail.Thread{
		Id: id,
		Messages: []*gmail.Message{
			{Payload: &gmail.MessagePart{Headers: []*gmail.MessagePartHeader{
				header("Message-ID", "<m1@mail>"),
				header("Subject", "Lunch"),
				header("From", "sender@example.com"),
			}}},
		},
	}
}

func TestThreadReplyTarget_DirectThreadID(t *testing.T) {
	svc := fakeGmailUsersService(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/threads/thread-1") {
			t.Errorf("unexpected request path %q; should not need the message fallback", r.URL.Path)
		}
		writeJSON(t, w, threadWithMessage("thread-1"))
	})
	s := &Service{svc: svc}

	target, threadID, resolvedFromMessage, err := s.threadReplyTarget("thread-1")
	if err != nil {
		t.Fatalf("threadReplyTarget: %v", err)
	}
	if threadID != "thread-1" {
		t.Errorf("threadID = %q, want thread-1", threadID)
	}
	if resolvedFromMessage != "" {
		t.Errorf("resolvedFromMessage = %q, want empty for a direct thread ID", resolvedFromMessage)
	}
	if target.Subject != "Lunch" {
		t.Errorf("target.Subject = %q, want Lunch", target.Subject)
	}
}

func TestThreadReplyTarget_FallsBackFromMessageID(t *testing.T) {
	svc := fakeGmailUsersService(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/threads/msg-mid"):
			// A mid-thread message ID 404s when treated as a thread ID.
			writeGoogleAPIError(t, w, http.StatusNotFound, "Not Found")
		case strings.Contains(r.URL.Path, "/messages/msg-mid"):
			writeJSON(t, w, &gmail.Message{Id: "msg-mid", ThreadId: "thread-1"})
		case strings.Contains(r.URL.Path, "/threads/thread-1"):
			writeJSON(t, w, threadWithMessage("thread-1"))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
	})
	s := &Service{svc: svc}

	_, threadID, resolvedFromMessage, err := s.threadReplyTarget("msg-mid")
	if err != nil {
		t.Fatalf("threadReplyTarget: %v", err)
	}
	if threadID != "thread-1" {
		t.Errorf("threadID = %q, want thread-1 (resolved from the message's ThreadId)", threadID)
	}
	if resolvedFromMessage != "msg-mid" {
		t.Errorf("resolvedFromMessage = %q, want msg-mid", resolvedFromMessage)
	}
}

func TestThreadReplyTarget_NeitherThreadNorMessageExists(t *testing.T) {
	svc := fakeGmailUsersService(t, func(w http.ResponseWriter, r *http.Request) {
		writeGoogleAPIError(t, w, http.StatusNotFound, "Not Found")
	})
	s := &Service{svc: svc}

	_, _, _, err := s.threadReplyTarget("does-not-exist")
	if err == nil {
		t.Fatal("expected an error when neither lookup resolves")
	}
	if !strings.Contains(err.Error(), "neither a thread nor a message") {
		t.Errorf("error = %q, want it to say neither a thread nor a message exists", err.Error())
	}
}

func TestThreadReplyTarget_NonNotFoundErrorSkipsFallback(t *testing.T) {
	svc := fakeGmailUsersService(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/messages/") {
			t.Error("a non-404 thread error must not trigger the message-ID fallback")
			http.Error(w, "unexpected fallback", http.StatusInternalServerError)
			return
		}
		writeGoogleAPIError(t, w, http.StatusBadRequest, "Invalid Id")
	})
	s := &Service{svc: svc}

	_, _, _, err := s.threadReplyTarget("malformed-id")
	if err == nil {
		t.Fatal("expected the 400 to surface as an error")
	}
	if strings.Contains(err.Error(), "neither a thread nor a message") {
		t.Errorf("a 400 should surface unchanged, not the fallback's not-found message: %q", err.Error())
	}
}

func TestIsNotFound(t *testing.T) {
	wrapped404 := fmt.Errorf("load thread abc: %w", &googleapi.Error{Code: 404, Message: "Not Found"})

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"googleapi 404", &googleapi.Error{Code: 404}, true},
		{"wrapped googleapi 404", wrapped404, true},
		{"googleapi 400", &googleapi.Error{Code: 400}, false},
		{"non-googleapi error", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNotFound(tt.err); got != tt.want {
				t.Errorf("isNotFound(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
