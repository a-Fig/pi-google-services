package gmail

import (
	"encoding/base64"
	"strings"
	"testing"

	"google.golang.org/api/gmail/v1"
)

func TestCreateMessage_NoAttachments(t *testing.T) {
	msg := createMessage("user@example.com", "Hello", "Body text", nil)
	if msg == nil || msg.Raw == "" {
		t.Fatal("expected non-empty message")
	}

	decoded, err := base64.URLEncoding.DecodeString(msg.Raw)
	if err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	str := string(decoded)

	if !strings.Contains(str, "Content-Type: text/plain") {
		t.Errorf("expected text/plain content type, got:\n%s", str)
	}
	if !strings.Contains(str, "To: user@example.com") {
		t.Errorf("missing To header")
	}
	if strings.Contains(str, "multipart/mixed") {
		t.Errorf("should not be multipart when no attachments")
	}
}

func TestCreateMessage_WithAttachments(t *testing.T) {
	attachments := []Attachment{
		{Filename: "doc.pdf", MimeType: "application/pdf", Data: []byte("fake-pdf-content")},
		{Filename: "note.txt", MimeType: "text/plain", Data: []byte("hello")},
	}

	msg := createMessage("user@example.com", "With file", "See attached", attachments)
	if msg == nil || msg.Raw == "" {
		t.Fatal("expected non-empty message")
	}

	decoded, err := base64.URLEncoding.DecodeString(msg.Raw)
	if err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	str := string(decoded)

	if !strings.Contains(str, "multipart/mixed") {
		t.Errorf("expected multipart/mixed, got:\n%s", str)
	}

	if !strings.Contains(str, `filename="doc.pdf"`) {
		t.Errorf("missing first attachment filename")
	}
	if !strings.Contains(str, `filename="note.txt"`) {
		t.Errorf("missing second attachment filename")
	}

	// Verify base64-encoded attachment data is present
	encPDF := base64.StdEncoding.EncodeToString([]byte("fake-pdf-content"))
	if !strings.Contains(str, encPDF) {
		t.Errorf("missing base64-encoded PDF data")
	}

	// Verify the body text is in a text/plain part
	if !strings.Contains(str, "See attached") {
		t.Errorf("missing body text in multipart message")
	}

	// Verify closing boundary
	if !strings.Contains(str, "--pi-google-") {
		t.Errorf("missing boundary markers")
	}
}

func TestCreateMessage_AttachmentDefaultMimeType(t *testing.T) {
	attachments := []Attachment{
		{Filename: "unknown.bin", Data: []byte("data")},
	}

	msg := createMessage("a@b.com", "S", "B", attachments)
	decoded, _ := base64.URLEncoding.DecodeString(msg.Raw)
	str := string(decoded)

	if !strings.Contains(str, "application/octet-stream") {
		t.Errorf("expected default mime type for attachment without MimeType")
	}
}

func TestEscapeFilename(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"normal.pdf", "normal.pdf"},
		{`file"name.pdf`, "file'name.pdf"},
		{"line\r\nbreak", "linebreak"},
	}
	for _, tt := range tests {
		got := escapeFilename(tt.input)
		if got != tt.expect {
			t.Errorf("escapeFilename(%q) = %q, want %q", tt.input, got, tt.expect)
		}
	}
}

func TestExtractAttachments(t *testing.T) {
	payload := &gmail.MessagePart{
		MimeType: "multipart/mixed",
		Parts: []*gmail.MessagePart{
			{
				MimeType: "text/plain",
				Body:     &gmail.MessagePartBody{Data: base64.URLEncoding.EncodeToString([]byte("body"))},
			},
			{
				PartId:   "1",
				MimeType: "application/pdf",
				Filename: "report.pdf",
				Body:     &gmail.MessagePartBody{AttachmentId: "att-1", Size: 2048},
			},
			{
				MimeType: "multipart/related",
				Parts: []*gmail.MessagePart{
					{
						PartId:   "2.0",
						MimeType: "image/png",
						Filename: "logo.png",
						Headers:  []*gmail.MessagePartHeader{{Name: "Content-Disposition", Value: "inline; filename=\"logo.png\""}},
						Body:     &gmail.MessagePartBody{AttachmentId: "att-2", Size: 512},
					},
				},
			},
		},
	}

	atts := extractAttachments(payload, 0)
	if len(atts) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(atts))
	}

	if atts[0].Filename != "report.pdf" || atts[0].AttachmentID != "att-1" || atts[0].Size != 2048 {
		t.Errorf("unexpected first attachment: %+v", atts[0])
	}
	if atts[0].Inline {
		t.Error("report.pdf should not be inline")
	}
	if atts[1].Filename != "logo.png" || !atts[1].Inline {
		t.Errorf("expected inline logo.png, got %+v", atts[1])
	}
}

func TestExtractAttachments_None(t *testing.T) {
	payload := &gmail.MessagePart{
		MimeType: "text/plain",
		Body:     &gmail.MessagePartBody{Data: base64.URLEncoding.EncodeToString([]byte("just text"))},
	}
	if atts := extractAttachments(payload, 0); len(atts) != 0 {
		t.Errorf("expected no attachments, got %d", len(atts))
	}
}

func TestExtractAttachments_InlineDataUsesPartID(t *testing.T) {
	// A small attachment whose bytes ride along in the part body has no
	// attachment ID, so the part ID addresses it instead.
	payload := &gmail.MessagePart{
		MimeType: "multipart/mixed",
		Parts: []*gmail.MessagePart{
			{
				PartId:   "1",
				MimeType: "text/csv",
				Filename: "small.csv",
				Body:     &gmail.MessagePartBody{Data: base64.URLEncoding.EncodeToString([]byte("a,b")), Size: 3},
			},
		},
	}

	atts := extractAttachments(payload, 0)
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(atts))
	}
	if atts[0].AttachmentID != "1" {
		t.Errorf("attachmentID = %q, want part ID %q", atts[0].AttachmentID, "1")
	}
}

func TestFindAttachmentPart(t *testing.T) {
	nested := &gmail.MessagePart{
		PartId:   "1.1",
		MimeType: "application/pdf",
		Filename: "deep.pdf",
		Body:     &gmail.MessagePartBody{AttachmentId: "att-deep"},
	}
	payload := &gmail.MessagePart{
		MimeType: "multipart/mixed",
		Parts: []*gmail.MessagePart{
			{MimeType: "multipart/alternative", Parts: []*gmail.MessagePart{nested}},
		},
	}

	if got := findAttachmentPart(payload, "att-deep", 0); got != nested {
		t.Errorf("expected to find nested part, got %+v", got)
	}
	if got := findAttachmentPart(payload, "missing", 0); got != nil {
		t.Errorf("expected nil for unknown ID, got %+v", got)
	}
}

func TestDecodeBase64URL_Unpadded(t *testing.T) {
	padded := base64.URLEncoding.EncodeToString([]byte("hello"))
	unpadded := base64.RawURLEncoding.EncodeToString([]byte("hello"))

	for _, in := range []string{padded, unpadded} {
		data, err := decodeBase64URL(in)
		if err != nil {
			t.Fatalf("decode %q: %v", in, err)
		}
		if string(data) != "hello" {
			t.Errorf("decoded = %q, want %q", string(data), "hello")
		}
	}
}
