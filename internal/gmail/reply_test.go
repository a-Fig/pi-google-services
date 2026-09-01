package gmail

import (
	"encoding/base64"
	"strings"
	"testing"

	"google.golang.org/api/gmail/v1"
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
