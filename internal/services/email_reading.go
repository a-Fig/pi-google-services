package services

import (
	"context"
	"encoding/json"
	"fmt"

	"strings"
	"unicode"

	"github.com/sombi/pi-google-services/internal/gmail"
	"github.com/sombi/pi-google-services/internal/mcp"
	"golang.org/x/net/html"
)

// Tokenization handles malformed mail HTML without treating script/style text as prose.
// Text tokens decode entities. Only HTML formatting whitespace is collapsed; plain text
// never passes through this function, preserving authored newlines and spacing.
func stripHTML(source string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(source))
	var out strings.Builder
	hidden := ""
	block := func(tag string) bool {
		switch tag {
		case "br", "p", "div", "li", "tr", "h1", "h2", "h3", "blockquote", "pre":
			return true
		}
		return false
	}
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			break
		}
		token := tokenizer.Token()
		if hidden != "" {
			if kind == html.EndTagToken && token.Data == hidden {
				hidden = ""
			}
			continue
		}
		if kind == html.StartTagToken || kind == html.SelfClosingTagToken {
			if token.Data == "script" || token.Data == "style" || token.Data == "head" {
				hidden = token.Data
				continue
			}
			if block(token.Data) {
				out.WriteByte('\n')
			}
		} else if kind == html.EndTagToken && block(token.Data) {
			out.WriteByte('\n')
		} else if kind == html.TextToken {
			space := false
			for _, r := range token.Data {
				if unicode.IsSpace(r) {
					if !space {
						out.WriteByte(' ')
					}
					space = true
				} else {
					out.WriteRune(r)
					space = false
				}
			}
		}
	}
	lines := strings.Split(out.String(), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

type bodyPage struct {
	ID          string                 `json:"id"`
	ThreadID    string                 `json:"threadId"`
	From        string                 `json:"from"`
	To          string                 `json:"to"`
	Subject     string                 `json:"subject"`
	Date        string                 `json:"date"`
	Body        string                 `json:"body"`
	Offset      int                    `json:"offset"`
	Total       int                    `json:"totalCharacters"`
	NextOffset  *int                   `json:"nextOffset,omitempty"`
	Attachments []gmail.AttachmentInfo `json:"attachments,omitempty"`
}

func emailPage(detail *gmail.EmailDetail, offset int) (*bodyPage, error) {
	body := detail.Body
	if detail.HTML {
		body = stripHTML(body)
	}
	chars := []rune(body)
	if offset < 0 || offset > len(chars) {
		return nil, fmt.Errorf("offset must be between 0 and %d", len(chars))
	}
	end := min(offset+5000, len(chars))
	page := &bodyPage{ID: detail.ID, ThreadID: detail.ThreadID, From: detail.From, To: detail.To, Subject: detail.Subject, Date: detail.Date, Body: string(chars[offset:end]), Offset: offset, Total: len(chars), Attachments: detail.Attachments}
	if end < len(chars) {
		page.NextOffset = &end
	}
	return page, nil
}
func jsonContent(value interface{}) interface{} {
	encoded, _ := json.Marshal(value)
	return contentResponse(string(encoded))
}
func (s *GmailService) handleGetThread(ctx context.Context, params json.RawMessage) (interface{}, *mcp.RPCError) {
	var args struct {
		ThreadID string `json:"threadId"`
		Offset   int    `json:"offset"`
	}
	if err := json.Unmarshal(params, &args); err != nil || args.ThreadID == "" || args.Offset < 0 {
		return nil, &mcp.RPCError{Code: -32602, Message: "threadId and nonnegative offset required"}
	}
	messages, err := s.api.GetThread(ctx, args.ThreadID)
	if err != nil {
		return nil, rpcError("get thread", err)
	}
	if args.Offset > len(messages) {
		return nil, &mcp.RPCError{Code: -32602, Message: "offset exceeds thread length"}
	}
	end := min(args.Offset+5, len(messages))
	pages := make([]*bodyPage, 0, end-args.Offset)
	for _, detail := range messages[args.Offset:end] {
		page, _ := emailPage(detail, 0)
		pages = append(pages, page)
	}
	result := map[string]interface{}{"threadId": args.ThreadID, "messages": pages, "totalMessages": len(messages)}
	if end < len(messages) {
		result["nextOffset"] = end
	}
	return jsonContent(result), nil
}
