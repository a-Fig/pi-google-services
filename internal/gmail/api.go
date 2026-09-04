// Package gmail wraps the Gmail API v1.
package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"mime"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// Service wraps the Gmail API client.
type Service struct {
	svc *gmail.UsersService
}

// EmailSummary is a lightweight email representation.
type EmailSummary struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"thread_id"`
	Subject      string   `json:"subject"`
	From         string   `json:"from"`
	To           string   `json:"to,omitempty"`
	Date         string   `json:"date"`
	Snippet      string   `json:"snippet"`
	ReturnPath   string   `json:"return_path,omitempty"`
	XForwardedTo string   `json:"x_forwarded_to,omitempty"`
	Origin       string   `json:"origin,omitempty"`
	LabelIDs     []string `json:"label_ids,omitempty"`
}

// EmailDetail is a full email with body content.
type EmailDetail struct {
	EmailSummary
	To          string           `json:"to"`
	Body        string           `json:"body"`
	HTML        bool             `json:"html"`
	Attachments []AttachmentInfo `json:"attachments,omitempty"`
}

// AttachmentInfo describes a file attached to a received message. Data is not
// included — fetch it with GetAttachment using AttachmentID.
type AttachmentInfo struct {
	AttachmentID string `json:"attachment_id"`
	Filename     string `json:"filename"`
	MimeType     string `json:"mime_type"`
	Size         int64  `json:"size"`
	Inline       bool   `json:"inline,omitempty"`
}

// New creates a Gmail Service from an OAuth2 token source.
func New(ctx context.Context, ts oauth2.TokenSource) (*Service, error) {
	svc, err := gmail.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("create gmail service: %w", err)
	}
	return &Service{svc: svc.Users}, nil
}

// ListInbox returns recent messages from the inbox.
func (s *Service) ListInbox(ctx context.Context, maxResults int64, query string) ([]*EmailSummary, error) {
	if maxResults <= 0 {
		maxResults = 20
	}

	call := s.svc.Messages.List("me").
		MaxResults(maxResults).
		LabelIds("INBOX")
	if query != "" {
		call.Q(query)
	}

	res, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}

	summaries := make([]*EmailSummary, 0, len(res.Messages))
	for _, m := range res.Messages {
		msg, err := s.svc.Messages.Get("me", m.Id).
			Format("metadata").
			MetadataHeaders("Subject", "From", "To", "Date", "Return-Path", "X-Forwarded-To").
			Do()
		if err != nil {
			continue // skip unreadable messages
		}

		summary := &EmailSummary{
			ID:       msg.Id,
			ThreadID: msg.ThreadId,
			Snippet:  msg.Snippet,
			LabelIDs: msg.LabelIds,
		}
		for _, h := range msg.Payload.Headers {
			switch h.Name {
			case "Subject":
				summary.Subject = h.Value
			case "From":
				summary.From = h.Value
			case "To":
				summary.To = h.Value
			case "Date":
				summary.Date = h.Value
			case "Return-Path":
				summary.ReturnPath = h.Value
			case "X-Forwarded-To":
				summary.XForwardedTo = h.Value
			}
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// GetEmail retrieves the full content of a message by ID.
func (s *Service) GetEmail(ctx context.Context, id string) (*EmailDetail, error) {
	msg, err := s.svc.Messages.Get("me", id).Format("full").Do()
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}

	detail := &EmailDetail{
		EmailSummary: EmailSummary{
			ID:       msg.Id,
			ThreadID: msg.ThreadId,
			Snippet:  msg.Snippet,
			LabelIDs: msg.LabelIds,
		},
	}

	for _, h := range msg.Payload.Headers {
		switch h.Name {
		case "Subject":
			detail.Subject = h.Value
		case "From":
			detail.From = h.Value
		case "Date":
			detail.Date = h.Value
		case "To":
			detail.To = h.Value
		}
	}

	// Extract body from the payload (prefer plain text)
	body, html := extractBody(msg.Payload, 0)
	detail.Body = body
	detail.HTML = html
	detail.Attachments = extractAttachments(msg.Payload, 0)

	return detail, nil
}

// GetAttachment downloads a single attachment from a received message. The
// attachmentID comes from EmailDetail.Attachments (GetEmail).
func (s *Service) GetAttachment(ctx context.Context, messageID, attachmentID string) (*Attachment, error) {
	msg, err := s.svc.Messages.Get("me", messageID).Format("full").Do()
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}

	part := findAttachmentPart(msg.Payload, attachmentID, 0)
	if part == nil {
		return nil, fmt.Errorf("attachment %s not found in message %s", attachmentID, messageID)
	}

	// Small attachments arrive inline in the part body; larger ones must be
	// fetched separately by attachment ID.
	raw := part.Body.Data
	if part.Body.AttachmentId != "" {
		body, err := s.svc.Messages.Attachments.Get("me", messageID, part.Body.AttachmentId).Do()
		if err != nil {
			return nil, fmt.Errorf("download attachment: %w", err)
		}
		raw = body.Data
	}

	data, err := decodeBase64URL(raw)
	if err != nil {
		return nil, fmt.Errorf("decode attachment data: %w", err)
	}

	return &Attachment{
		Filename: part.Filename,
		MimeType: part.MimeType,
		Data:     data,
	}, nil
}

// Attachment represents a file to attach to an email.
type Attachment struct {
	Filename string
	MimeType string
	Data     []byte
}

// SendEmail sends a new email with optional attachments.
func (s *Service) SendEmail(ctx context.Context, to, subject, body string, attachments []Attachment) (*gmail.Message, error) {
	msg := createMessage(to, subject, body, attachments)
	sent, err := s.svc.Messages.Send("me", msg).Do()
	if err != nil {
		return nil, fmt.Errorf("send message: %w", err)
	}
	return sent, nil
}

// SearchEmails searches messages by query.
func (s *Service) SearchEmails(ctx context.Context, query string, maxResults int64) ([]*EmailSummary, error) {
	return s.ListInbox(ctx, maxResults, query)
}

// --- helpers ---

func extractBody(part *gmail.MessagePart, depth int) (body string, html bool) {
	if part == nil || depth > 5 {
		return "", false
	}

	// Check this part's body
	if part.MimeType == "text/plain" && part.Body != nil && part.Body.Data != "" {
		data, _ := decodeBase64URL(part.Body.Data)
		return string(data), false
	}
	if part.MimeType == "text/html" && part.Body != nil && part.Body.Data != "" {
		data, _ := decodeBase64URL(part.Body.Data)
		return string(data), true
	}

	// Recurse into child parts
	for _, child := range part.Parts {
		b, h := extractBody(child, depth+1)
		if b != "" {
			return b, h
		}
	}
	return "", false
}

// extractAttachments walks a message payload collecting every part that carries
// a filename. Both regular attachments and inline images (cid: references) are
// returned; Inline distinguishes them.
func extractAttachments(part *gmail.MessagePart, depth int) []AttachmentInfo {
	if part == nil || depth > 10 {
		return nil
	}

	var found []AttachmentInfo
	if part.Filename != "" && part.Body != nil {
		id := part.Body.AttachmentId
		if id == "" {
			// Inline body data — address the part by its ID instead.
			id = part.PartId
		}
		if id != "" {
			found = append(found, AttachmentInfo{
				AttachmentID: id,
				Filename:     part.Filename,
				MimeType:     part.MimeType,
				Size:         part.Body.Size,
				Inline:       isInline(part),
			})
		}
	}

	for _, child := range part.Parts {
		found = append(found, extractAttachments(child, depth+1)...)
	}
	return found
}

// findAttachmentPart locates the part an attachment ID refers to. The ID is
// either a Gmail attachment ID or, for inline data, a MIME part ID.
func findAttachmentPart(part *gmail.MessagePart, attachmentID string, depth int) *gmail.MessagePart {
	if part == nil || depth > 10 {
		return nil
	}

	if part.Filename != "" && part.Body != nil {
		if part.Body.AttachmentId == attachmentID || (part.Body.AttachmentId == "" && part.PartId == attachmentID) {
			return part
		}
	}

	for _, child := range part.Parts {
		if hit := findAttachmentPart(child, attachmentID, depth+1); hit != nil {
			return hit
		}
	}
	return nil
}

// isInline reports whether a part is displayed within the message body
// (an embedded image) rather than offered as a separate download.
func isInline(part *gmail.MessagePart) bool {
	for _, h := range part.Headers {
		if strings.EqualFold(h.Name, "Content-Disposition") {
			return strings.HasPrefix(strings.TrimSpace(strings.ToLower(h.Value)), "inline")
		}
	}
	return false
}

// decodeBase64URL decodes Gmail's base64url payloads, which may be unpadded.
func decodeBase64URL(s string) ([]byte, error) {
	if data, err := base64.URLEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}

func createMessage(to, subject, body string, attachments []Attachment) *gmail.Message {
	return buildMessage(to, subject, body, attachments, "")
}

// buildMessage assembles the RFC 5322 message. extraHeaders is inserted verbatim
// after Subject and must already be CRLF-terminated; replies use it to carry
// In-Reply-To and References.
func buildMessage(to, subject, body string, attachments []Attachment, extraHeaders string) *gmail.Message {
	encSubject := mime.BEncoding.Encode("UTF-8", subject)

	if len(attachments) == 0 {
		msg := fmt.Sprintf("From: me\r\nTo: %s\r\nSubject: %s\r\n%sMIME-Version: 1.0\r\nContent-Type: text/plain; charset=\"UTF-8\"\r\n\r\n%s", to, encSubject, extraHeaders, body)
		encoded := base64.URLEncoding.EncodeToString([]byte(msg))
		return &gmail.Message{Raw: encoded}
	}

	boundary := fmt.Sprintf("pi-google-%d", time.Now().UnixNano())

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: me\r\nTo: %s\r\nSubject: %s\r\n%sMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", to, encSubject, extraHeaders, boundary)

	fmt.Fprintf(&buf, "--%s\r\nContent-Type: text/plain; charset=\"UTF-8\"\r\n\r\n%s\r\n", boundary, body)

	for _, att := range attachments {
		mt := att.MimeType
		if mt == "" {
			mt = "application/octet-stream"
		}
		fmt.Fprintf(&buf, "--%s\r\n", boundary)
		fmt.Fprintf(&buf, "Content-Type: %s\r\n", mt)
		fmt.Fprintf(&buf, "Content-Transfer-Encoding: base64\r\n")
		fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=\"%s\"\r\n\r\n", escapeFilename(att.Filename))

		encoded := base64.StdEncoding.EncodeToString(att.Data)
		for i := 0; i < len(encoded); i += 76 {
			end := i + 76
			if end > len(encoded) {
				end = len(encoded)
			}
			buf.WriteString(encoded[i:end])
			buf.WriteString("\r\n")
		}
	}

	fmt.Fprintf(&buf, "--%s--\r\n", boundary)

	encoded := base64.URLEncoding.EncodeToString(buf.Bytes())
	return &gmail.Message{Raw: encoded}
}

// escapeFilename removes characters that would break the Content-Disposition header.
func escapeFilename(name string) string {
	name = strings.ReplaceAll(name, "\"", "'")
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	return name
}

// HumanDate parses and reformats RFC1123 dates for display.
func HumanDate(raw string) string {
	t, err := time.Parse(time.RFC1123Z, raw)
	if err != nil {
		t, err = time.Parse("Mon, 2 Jan 2006 15:04:05 -0700", raw)
		if err != nil {
			return raw
		}
	}
	if t.After(time.Now().Add(-24 * time.Hour)) {
		return t.Format("15:04")
	}
	return t.Format("Jan 2")
}
