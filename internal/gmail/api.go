// Package gmail wraps the Gmail API v1.
package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand/v2"
	"mime"
	"mime/quotedprintable"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// Service wraps the Gmail API client.
type Service struct {
	svc *gmail.UsersService
}

// EmailSummary is a lightweight email representation.
type EmailSummary struct {
	ID           string `json:"id"`
	ThreadID     string `json:"thread_id"`
	Subject      string `json:"subject"`
	From         string `json:"from"`
	To           string `json:"to,omitempty"`
	Date         string `json:"date"`
	Snippet      string `json:"snippet"`
	ReturnPath   string `json:"return_path,omitempty"`
	XForwardedTo string `json:"x_forwarded_to,omitempty"`
	Origin       string `json:"origin,omitempty"`
	// AuthenticationResults concatenates every Authentication-Results header on
	// the message with "; " (Gmail can add more than one, e.g. one per relay
	// hop). Absent (and omitted from JSON) when the message carries none.
	AuthenticationResults string `json:"authentication_results,omitempty"`
	// Dmarc is parsed out of AuthenticationResults ("pass", "fail", "none", or
	// "" when no dmarc= result is present). Unlike AuthenticationResults this
	// is never omitted: its mere presence in the JSON output (even as "") is
	// the signal a caller uses to tell "this connector build evaluates DMARC"
	// from "this connector predates DMARC support entirely" (the latter omits
	// the key outright, since older builds have no such struct field to
	// marshal). See CONNECTOR.md section 5.
	Dmarc    string   `json:"dmarc"`
	LabelIDs []string `json:"label_ids,omitempty"`
}

// dmarcResultPattern extracts the result token from a `dmarc=<token>` clause
// inside an Authentication-Results header, e.g. "dmarc=pass (p=REJECT ...)".
var dmarcResultPattern = regexp.MustCompile(`(?i)dmarc=([a-zA-Z]+)`)

// parseDmarc pulls the DMARC verdict out of a (possibly multi-header,
// "; "-joined) Authentication-Results value. Only "pass", "fail" and "none"
// are recognized and returned verbatim in lowercase; anything else Gmail
// might stamp (quarantine, reject, temperror, permerror) or the header being
// absent/unparseable all collapse to "" rather than being guessed at, since a
// caller gating trust decisions on this value should not have to know every
// DMARC result token that exists to be safe.
func parseDmarc(authenticationResults string) string {
	m := dmarcResultPattern.FindStringSubmatch(authenticationResults)
	if m == nil {
		return ""
	}
	switch strings.ToLower(m[1]) {
	case "pass":
		return "pass"
	case "fail":
		return "fail"
	case "none":
		return "none"
	default:
		return ""
	}
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

	res, err := call.Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}

	// One Messages.Get per message, each its own round trip. Run one after another, a 30-message
	// inbox cost 31 round trips from the phone before anything showed (pi-vi #324). They run
	// listFetchConcurrency at a time instead, multiplexed on the client's one HTTP/2 connection,
	// and land in list order.
	//
	// A message that cannot be read, for any reason but being gone, fails the whole list and stops
	// the fetches still to come. A list quietly missing messages is worse than an error:
	// email-watch's first poll of a rule takes what it gets as everything there is, and would
	// later report a dropped message as new mail.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var failOnce sync.Once
	var failed error
	fail := func(err error) {
		failOnce.Do(func() {
			failed = err
			cancel()
		})
	}
	fetched := make([]*EmailSummary, len(res.Messages))
	sem := make(chan struct{}, listFetchConcurrency)
	var wg sync.WaitGroup
	for i, m := range res.Messages {
		sem <- struct{}{}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			summary, err := s.summary(ctx, id)
			if err != nil {
				fail(err)
				return
			}
			fetched[i] = summary
		}(i, m.Id)
	}
	wg.Wait()
	if failed != nil {
		return nil, failed
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}

	summaries := make([]*EmailSummary, 0, len(fetched))
	for _, summary := range fetched {
		if summary != nil {
			summaries = append(summaries, summary)
		}
	}
	return summaries, nil
}

const (
	// listFetchConcurrency bounds ListInbox's parallel Messages.Get calls: a page of 30 takes six
	// round trips rather than 30. A get costs 5 of the 250 quota units a user may spend per
	// second, and email-watch polls from its own process beside this one, so at most twice this
	// many are in flight for one account.
	listFetchConcurrency = 5
	// listFetchAttempts is how many times one message's get is tried before the list fails.
	listFetchAttempts = 3
	// maxRetryDelay caps the wait a Retry-After header can ask for.
	maxRetryDelay = 10 * time.Second
)

// fetchRetryDelay is the wait before a get's second try, doubled for each try after; a variable
// so tests need not sit through it.
var fetchRetryDelay = 500 * time.Millisecond

// summary fetches one message's metadata for ListInbox. A message Gmail no longer has, deleted
// between the list and the get, comes back nil and is skipped, as before. Rate limits, Gmail's
// server errors and requests that got no answer are tried again; anything else is an error.
func (s *Service) summary(ctx context.Context, id string) (*EmailSummary, error) {
	var msg *gmail.Message
	for attempt := 1; ; attempt++ {
		var err error
		msg, err = s.svc.Messages.Get("me", id).
			Format("metadata").
			MetadataHeaders("Subject", "From", "To", "Date", "Return-Path", "X-Forwarded-To", "Authentication-Results").
			Context(ctx).
			Do()
		if err == nil {
			break
		}
		if isNotFound(err) {
			return nil, nil
		}
		if attempt == listFetchAttempts || !retryableFetch(ctx, err) {
			return nil, fmt.Errorf("get message %s: %w", id, err)
		}
		timer := time.NewTimer(retryDelay(err, attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("get message %s: %w", id, ctx.Err())
		case <-timer.C:
		}
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
		case "Authentication-Results":
			if summary.AuthenticationResults != "" {
				summary.AuthenticationResults += "; " + h.Value
			} else {
				summary.AuthenticationResults = h.Value
			}
		}
	}
	summary.Dmarc = parseDmarc(summary.AuthenticationResults)
	return summary, nil
}

// retryableFetch says whether a failed get is worth another try: Gmail's rate limits (429, or
// 403 with a rate-limit reason), its 5xx answers, and a request that got no answer at all. Never
// once the caller has given up.
func retryableFetch(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var gErr *googleapi.Error
	if !errors.As(err, &gErr) {
		return true
	}
	if gErr.Code == http.StatusTooManyRequests || gErr.Code >= 500 {
		return true
	}
	if gErr.Code == http.StatusForbidden {
		for _, item := range gErr.Errors {
			if item.Reason == "rateLimitExceeded" || item.Reason == "userRateLimitExceeded" {
				return true
			}
		}
	}
	return false
}

// retryDelay is how long to wait before the next try: what Gmail asked for in Retry-After, if it
// said, or a doubling delay with jitter, so that fetches turned away together do not all come
// back together.
func retryDelay(err error, attempt int) time.Duration {
	var gErr *googleapi.Error
	if errors.As(err, &gErr) {
		if secs, convErr := strconv.Atoi(gErr.Header.Get("Retry-After")); convErr == nil && secs >= 0 {
			return min(time.Duration(secs)*time.Second, maxRetryDelay)
		}
	}
	d := fetchRetryDelay << (attempt - 1)
	return d + rand.N(d)
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
		case "Authentication-Results":
			if detail.AuthenticationResults != "" {
				detail.AuthenticationResults += "; " + h.Value
			} else {
				detail.AuthenticationResults = h.Value
			}
		}
	}
	detail.Dmarc = parseDmarc(detail.AuthenticationResults)

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
		if attachmentID == rootAttachmentID || mimePartIDPattern.MatchString(attachmentID) {
			return nil, fmt.Errorf("attachment %s not found in message %s; read the message again for its attachment IDs", attachmentID, messageID)
		}
		// Older tool results contain Gmail's opaque attachment ID, which may no
		// longer appear in this fetch. Let Gmail resolve it directly. Its attachment
		// endpoint returns only bytes, so do not guess another part's metadata.
		part = &gmail.MessagePart{
			Filename: "attachment.bin",
			MimeType: "application/octet-stream",
			Body:     &gmail.MessagePartBody{AttachmentId: attachmentID},
		}
	}

	// Small attachments arrive inline in the part body; larger ones must be
	// fetched separately by attachment ID.
	raw := part.Body.Data
	if part.Body.AttachmentId != "" {
		body, err := s.svc.Messages.Attachments.Get("me", messageID, part.Body.AttachmentId).Do()
		if err != nil {
			return nil, fmt.Errorf("download attachment (read the message again if this ID has expired): %w", err)
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

// Gmail's root MIME part can have an empty PartId. Give it a stable handle too.
const rootAttachmentID = "part:root"

var mimePartIDPattern = regexp.MustCompile(`^\d+(\.\d+)*$`)

// extractAttachments walks a message payload collecting every part that carries
// a filename. Both regular attachments and inline images (cid: references) are
// returned; Inline distinguishes them.
func extractAttachments(part *gmail.MessagePart, depth int) []AttachmentInfo {
	if part == nil || depth > 10 {
		return nil
	}

	var found []AttachmentInfo
	if part.Filename != "" && part.Body != nil {
		// The part ID is the handle. Gmail's attachment ID cannot be one: messages.get gives the
		// same attachment a different ID on every call, so an ID listed here never matched the
		// fetch GetAttachment makes later and every download ended in "not found".
		id := part.PartId
		if id == "" && depth == 0 {
			id = rootAttachmentID
		}
		if id == "" {
			id = part.Body.AttachmentId
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

// findAttachmentPart locates the part an attachment ID refers to. The ID is a
// MIME part ID (including the root handle), or a legacy Gmail attachment ID
// still present in this fetch. Unmatched opaque IDs are tried directly by GetAttachment.
func findAttachmentPart(part *gmail.MessagePart, attachmentID string, depth int) *gmail.MessagePart {
	if part == nil || depth > 10 || attachmentID == "" {
		return nil
	}

	if part.Filename != "" && part.Body != nil {
		if part.PartId == attachmentID || part.Body.AttachmentId == attachmentID ||
			(depth == 0 && part.PartId == "" && attachmentID == rootAttachmentID) {
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
	// Encode the text part, not just Gmail's outer base64url envelope. Soft MIME
	// line breaks keep long paragraphs within transport limits without adding
	// visible newlines, and carry UTF-8 safely through 7-bit mail transports.
	var text bytes.Buffer
	writer := quotedprintable.NewWriter(&text)
	_, _ = writer.Write([]byte(body)) // bytes.Buffer writes cannot fail.
	_ = writer.Close()                // Flush the final line before reading text.
	encodedBody := text.String()

	if len(attachments) == 0 {
		msg := fmt.Sprintf("From: me\r\nTo: %s\r\nSubject: %s\r\n%sMIME-Version: 1.0\r\nContent-Type: text/plain; charset=\"UTF-8\"\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n%s", to, encSubject, extraHeaders, encodedBody)
		encoded := base64.URLEncoding.EncodeToString([]byte(msg))
		return &gmail.Message{Raw: encoded}
	}

	boundary := fmt.Sprintf("pi-google-%d", time.Now().UnixNano())

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: me\r\nTo: %s\r\nSubject: %s\r\n%sMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", to, encSubject, extraHeaders, boundary)

	fmt.Fprintf(&buf, "--%s\r\nContent-Type: text/plain; charset=\"UTF-8\"\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n%s\r\n", boundary, encodedBody)

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
