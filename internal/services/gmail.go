package services

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/sombi/pi-google-services/internal/drive"
	"github.com/sombi/pi-google-services/internal/gmail"
	"github.com/sombi/pi-google-services/internal/mcp"
)

// GmailService implements the Service interface for Gmail.
type GmailService struct {
	api      *gmail.Service
	driveAPI *drive.Service
}

// NewGmail creates a GmailService from the Gmail API wrapper.
// driveAPI is optional — when provided, email attachments can reference
// Google Drive file IDs. Pass nil to support local-file attachments only.
func NewGmail(api *gmail.Service, driveAPI *drive.Service) *GmailService {
	return &GmailService{api: api, driveAPI: driveAPI}
}

func (s *GmailService) Name() string { return "gmail" }

func (s *GmailService) Scopes() []string {
	return []string{
		"https://www.googleapis.com/auth/gmail.readonly",
		"https://www.googleapis.com/auth/gmail.send",
		"https://www.googleapis.com/auth/gmail.modify",
	}
}

func (s *GmailService) Tools() []mcp.ToolDefinition {
	return []mcp.ToolDefinition{
		{
			Name:        "list-inbox",
			Description: "List recent emails from inbox",
			InputSchema: mcp.InputSchema{
				Type: "object",
				Properties: map[string]mcp.PropertySchema{
					"maxResults": {Type: "number", Description: "Max emails (default: 20)", Default: 20},
					"query":      {Type: "string", Description: "Optional search filter"},
				},
			},
		},
		{
			Name:        "get-email",
			Description: "Read a full email by ID",
			InputSchema: mcp.InputSchema{
				Type: "object",
				Properties: map[string]mcp.PropertySchema{
					"id": {Type: "string", Description: "Email message ID"},
				},
				Required: []string{"id"},
			},
		},
		{
			Name:        "search-emails",
			Description: "Search emails by query",
			InputSchema: mcp.InputSchema{
				Type: "object",
				Properties: map[string]mcp.PropertySchema{
					"query":      {Type: "string", Description: "Search query (Gmail syntax)"},
					"maxResults": {Type: "number", Description: "Max results (default: 20)", Default: 20},
				},
				Required: []string{"query"},
			},
		},
		{
			Name:        "send-email",
			Description: "Send an email with optional file attachments",
			InputSchema: mcp.InputSchema{
				Type: "object",
				Properties: map[string]mcp.PropertySchema{
					"to":      {Type: "string", Description: "Recipient email"},
					"subject": {Type: "string", Description: "Email subject"},
					"body":    {Type: "string", Description: "Email body text"},
					"attachments": {
						Type:        "array",
						Description: "Files to attach. Each item can have 'localPath' (local file) or 'driveFileId' (Google Drive file ID).",
						Items: &mcp.PropertySchema{
							Type: "object",
							Properties: map[string]mcp.PropertySchema{
								"localPath":   {Type: "string", Description: "Local file path to attach"},
								"driveFileId": {Type: "string", Description: "Google Drive file ID to attach"},
							},
						},
					},
				},
				Required: []string{"to", "subject", "body"},
			},
		},
		{
			Name:        "reply-to-email",
			Description: "Reply in-thread to an email, with optional file attachments. Recipient and subject come from the thread so the reply actually threads; normally pass only threadId and body.",
			InputSchema: mcp.InputSchema{
				Type: "object",
				Properties: map[string]mcp.PropertySchema{
					"threadId": {Type: "string", Description: "Thread ID to reply to (thread_id from list-inbox, get-email, or search-emails)"},
					"to":       {Type: "string", Description: "Optional. Recipient email. Defaults to the thread's Reply-To, else its From."},
					"subject":  {Type: "string", Description: "Optional and normally omitted. Ignored when the thread has a subject: a reply whose subject differs reads as a separate email in most mail clients."},
					"body":     {Type: "string", Description: "Reply body text"},
					"attachments": {
						Type:        "array",
						Description: "Files to attach. Each item can have 'localPath' (local file) or 'driveFileId' (Google Drive file ID).",
						Items: &mcp.PropertySchema{
							Type: "object",
							Properties: map[string]mcp.PropertySchema{
								"localPath":   {Type: "string", Description: "Local file path to attach"},
								"driveFileId": {Type: "string", Description: "Google Drive file ID to attach"},
							},
						},
					},
				},
				Required: []string{"threadId", "body"},
			},
		},
		{
			Name:        "download-attachment",
			Description: "Download an attachment from a received email to a local file",
			InputSchema: mcp.InputSchema{
				Type: "object",
				Properties: map[string]mcp.PropertySchema{
					"messageId":    {Type: "string", Description: "Email message ID (from list-inbox or search-emails)"},
					"attachmentId": {Type: "string", Description: "Attachment ID listed by get-email"},
					"savePath":     {Type: "string", Description: "Destination file, or a directory (trailing slash) to save under the original filename. ~ is expanded. Defaults to the system temp directory. An existing file is never overwritten; a suffix is added instead. Hidden paths such as ~/.ssh are refused."},
				},
				Required: []string{"messageId", "attachmentId"},
			},
		},
	}
}

func (s *GmailService) Handle(ctx context.Context, toolName string, params json.RawMessage) (interface{}, *mcp.RPCError) {
	switch toolName {
	case "list-inbox":
		return s.handleListInbox(ctx, params)
	case "get-email":
		return s.handleGetEmail(ctx, params)
	case "search-emails":
		return s.handleSearchEmails(ctx, params)
	case "send-email":
		return s.handleSendEmail(ctx, params)
	case "reply-to-email":
		return s.handleReplyEmail(ctx, params)
	case "download-attachment":
		return s.handleDownloadAttachment(ctx, params)
	default:
		return nil, &mcp.RPCError{Code: -32601, Message: fmt.Sprintf("Gmail tool not found: %s", toolName)}
	}
}

// --- handlers ---

func (s *GmailService) handleListInbox(ctx context.Context, params json.RawMessage) (interface{}, *mcp.RPCError) {
	var args struct {
		MaxResults int64  `json:"maxResults"`
		Query      string `json:"query"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, &mcp.RPCError{Code: -32602, Message: "Invalid arguments", Data: err.Error()}
	}

	msgs, err := s.api.ListInbox(ctx, args.MaxResults, args.Query)
	if err != nil {
		return nil, &mcp.RPCError{Code: -32603, Message: "Failed to list inbox", Data: err.Error()}
	}

	var b strings.Builder
	if len(msgs) == 0 {
		b.WriteString("📭 Inbox vacío.")
	} else {
		for i, m := range msgs {
			date := gmail.HumanDate(m.Date)
			b.WriteString(fmt.Sprintf("%d. %s\n   📧 %s\n   👤 %s  🕐 %s\n   💬 %s\n",
				i+1, m.Subject, m.ID, m.From, date, m.Snippet))
		}
	}

	return contentResponse(b.String()), nil
}

func (s *GmailService) handleGetEmail(ctx context.Context, params json.RawMessage) (interface{}, *mcp.RPCError) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, &mcp.RPCError{Code: -32602, Message: "Invalid arguments", Data: err.Error()}
	}
	if args.ID == "" {
		return nil, &mcp.RPCError{Code: -32602, Message: "id required"}
	}

	detail, err := s.api.GetEmail(ctx, args.ID)
	if err != nil {
		return nil, &mcp.RPCError{Code: -32603, Message: "Failed to get email", Data: err.Error()}
	}

	body := detail.Body
	if detail.HTML {
		body = stripHTML(body)
	}
	if len(body) > 5000 {
		body = body[:5000] + "\n\n[...truncated at 5000 chars]"
	}

	result := fmt.Sprintf("📧 %s\nFrom: %s\nTo: %s\nDate: %s\n\n%s",
		detail.Subject, detail.From, detail.To, detail.Date, body)
	result += formatAttachments(detail.Attachments)

	return contentResponse(result), nil
}

func (s *GmailService) handleSearchEmails(ctx context.Context, params json.RawMessage) (interface{}, *mcp.RPCError) {
	var args struct {
		Query      string `json:"query"`
		MaxResults int64  `json:"maxResults"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, &mcp.RPCError{Code: -32602, Message: "Invalid arguments", Data: err.Error()}
	}
	if args.Query == "" {
		return nil, &mcp.RPCError{Code: -32602, Message: "query required"}
	}

	msgs, err := s.api.SearchEmails(ctx, args.Query, args.MaxResults)
	if err != nil {
		return nil, &mcp.RPCError{Code: -32603, Message: "Failed to search", Data: err.Error()}
	}

	var b strings.Builder
	if len(msgs) == 0 {
		b.WriteString("No results.")
	} else {
		for i, m := range msgs {
			date := gmail.HumanDate(m.Date)
			b.WriteString(fmt.Sprintf("%d. [%s] %s\n   From: %s  %s\n   %s\n",
				i+1, m.ID, m.Subject, m.From, date, m.Snippet))
		}
	}

	return contentResponse(b.String()), nil
}

func (s *GmailService) handleSendEmail(ctx context.Context, params json.RawMessage) (interface{}, *mcp.RPCError) {
	var args struct {
		To          string            `json:"to"`
		Subject     string            `json:"subject"`
		Body        string            `json:"body"`
		Attachments []attachmentInput `json:"attachments"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, &mcp.RPCError{Code: -32602, Message: "Invalid arguments", Data: err.Error()}
	}
	if args.To == "" || args.Subject == "" {
		return nil, &mcp.RPCError{Code: -32602, Message: "to and subject required"}
	}

	attachments, attErr := s.resolveAttachments(ctx, args.Attachments)
	if attErr != nil {
		return nil, attErr
	}

	sent, err := s.api.SendEmail(ctx, args.To, args.Subject, args.Body, attachments)
	if err != nil {
		return nil, &mcp.RPCError{Code: -32603, Message: "Failed to send", Data: err.Error()}
	}

	result := fmt.Sprintf("✅ Email sent to %s\nID: %s", args.To, sent.Id)
	if len(attachments) > 0 {
		result += fmt.Sprintf("\n📎 Attachments: %d", len(attachments))
	}
	return contentResponse(result), nil
}

func (s *GmailService) handleReplyEmail(ctx context.Context, params json.RawMessage) (interface{}, *mcp.RPCError) {
	var args struct {
		ThreadID    string            `json:"threadId"`
		To          string            `json:"to"`
		Subject     string            `json:"subject"`
		Body        string            `json:"body"`
		Attachments []attachmentInput `json:"attachments"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, &mcp.RPCError{Code: -32602, Message: "Invalid arguments", Data: err.Error()}
	}
	if args.ThreadID == "" || args.Body == "" {
		return nil, &mcp.RPCError{Code: -32602, Message: "threadId and body required"}
	}

	attachments, attErr := s.resolveAttachments(ctx, args.Attachments)
	if attErr != nil {
		return nil, attErr
	}

	reply, err := s.api.ReplyToEmail(ctx, args.ThreadID, args.To, args.Subject, args.Body, attachments)
	if err != nil {
		return nil, &mcp.RPCError{Code: -32603, Message: "Failed to reply", Data: err.Error()}
	}

	result := fmt.Sprintf("✅ Reply sent to %s\nSubject: %s\nID: %s\nThread: %s",
		reply.To, reply.Subject, reply.Message.Id, reply.Message.ThreadId)
	if !reply.Threaded {
		// Gmail accepted the message but did not attach it to the conversation, so
		// the recipient would see a new email. Report that instead of plain success.
		result += "\nWarning: Gmail did not add this to the requested thread; it was delivered as a new conversation."
	}
	if len(attachments) > 0 {
		result += fmt.Sprintf("\n📎 Attachments: %d", len(attachments))
	}
	return contentResponse(result), nil
}

func (s *GmailService) handleDownloadAttachment(ctx context.Context, params json.RawMessage) (interface{}, *mcp.RPCError) {
	var args struct {
		MessageID    string `json:"messageId"`
		AttachmentID string `json:"attachmentId"`
		SavePath     string `json:"savePath"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, &mcp.RPCError{Code: -32602, Message: "Invalid arguments", Data: err.Error()}
	}
	if args.MessageID == "" || args.AttachmentID == "" {
		return nil, &mcp.RPCError{Code: -32602, Message: "messageId and attachmentId required"}
	}

	att, err := s.api.GetAttachment(ctx, args.MessageID, args.AttachmentID)
	if err != nil {
		return nil, &mcp.RPCError{Code: -32603, Message: "Failed to download attachment", Data: err.Error()}
	}

	dest, err := resolveSavePath(args.SavePath, att.Filename)
	if err != nil {
		return nil, &mcp.RPCError{Code: -32602, Message: "Refusing that destination", Data: err.Error()}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return nil, &mcp.RPCError{Code: -32603, Message: "Failed to create destination directory", Data: err.Error()}
	}
	file, dest, err := openExclusive(dest)
	if err != nil {
		return nil, &mcp.RPCError{Code: -32603, Message: fmt.Sprintf("Failed to create %s", dest), Data: err.Error()}
	}
	if _, err := file.Write(att.Data); err != nil {
		file.Close()
		return nil, &mcp.RPCError{Code: -32603, Message: fmt.Sprintf("Failed to write %s", dest), Data: err.Error()}
	}
	if err := file.Close(); err != nil {
		return nil, &mcp.RPCError{Code: -32603, Message: fmt.Sprintf("Failed to close %s", dest), Data: err.Error()}
	}

	result := fmt.Sprintf("💾 Saved %s (%s, %s)\n📁 %s",
		att.Filename, att.MimeType, fmtSize(int64(len(att.Data))), dest)
	return contentResponse(result), nil
}

// formatAttachments renders the attachment list appended to a read email.
func formatAttachments(atts []gmail.AttachmentInfo) string {
	if len(atts) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n\n📎 Attachments (%d):\n", len(atts))
	for i, a := range atts {
		label := ""
		if a.Inline {
			label = " (inline)"
		}
		fmt.Fprintf(&b, "%d. %s — %s, %s%s\n   id: %s\n",
			i+1, a.Filename, a.MimeType, fmtSize(a.Size), label, a.AttachmentID)
	}
	b.WriteString("\nUse download-attachment with the message ID and an attachment id to save one.")
	return b.String()
}

// attachmentInput is the JSON shape accepted by the tool schema.
type attachmentInput struct {
	LocalPath   string `json:"localPath"`
	DriveFileID string `json:"driveFileId"`
}

// resolveAttachments converts attachment input params into gmail.Attachment
// structs, reading local files or downloading from Drive as needed.
func (s *GmailService) resolveAttachments(ctx context.Context, inputs []attachmentInput) ([]gmail.Attachment, *mcp.RPCError) {
	if len(inputs) == 0 {
		return nil, nil
	}

	attachments := make([]gmail.Attachment, 0, len(inputs))
	for i, input := range inputs {
		switch {
		case input.LocalPath != "":
			data, err := os.ReadFile(input.LocalPath)
			if err != nil {
				return nil, &mcp.RPCError{
					Code:    -32603,
					Message: fmt.Sprintf("Failed to read attachment %d: %s", i+1, input.LocalPath),
					Data:    err.Error(),
				}
			}
			mimeType := mime.TypeByExtension(filepath.Ext(input.LocalPath))
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}
			attachments = append(attachments, gmail.Attachment{
				Filename: filepath.Base(input.LocalPath),
				MimeType: mimeType,
				Data:     data,
			})

		case input.DriveFileID != "":
			if s.driveAPI == nil {
				return nil, &mcp.RPCError{
					Code:    -32603,
					Message: "Drive attachments not available (Drive API not configured)",
				}
			}
			content, err := s.driveAPI.DownloadContent(ctx, input.DriveFileID)
			if err != nil {
				return nil, &mcp.RPCError{
					Code:    -32603,
					Message: fmt.Sprintf("Failed to download Drive file %s", input.DriveFileID),
					Data:    err.Error(),
				}
			}
			attachments = append(attachments, gmail.Attachment{
				Filename: content.Name,
				MimeType: content.MimeType,
				Data:     content.Data,
			})

		default:
			return nil, &mcp.RPCError{
				Code:    -32602,
				Message: fmt.Sprintf("Attachment %d must have 'localPath' or 'driveFileId'", i+1),
			}
		}
	}
	return attachments, nil
}

// stripHTML removes HTML tags for plain-text display.
func stripHTML(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
