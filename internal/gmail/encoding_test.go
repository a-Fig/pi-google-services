package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"strings"
	"testing"

	"google.golang.org/api/gmail/v1"
)

// Decode the actual RFC 5322 wire message, including the text part's own CTE.
// Gmail's outer base64url encoding does not protect the decoded MIME body.
func assertTextEncoding(t *testing.T, msg *gmail.Message, body string, attachments []Attachment) *mail.Message {
	t.Helper()
	raw, err := base64.URLEncoding.DecodeString(msg.Raw)
	if err != nil {
		t.Fatal(err)
	}
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	textHeader, textReader := message.Header, message.Body
	var parts *multipart.Reader
	if len(attachments) > 0 {
		mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/mixed" {
			t.Fatalf("multipart Content-Type = %q: %v", mediaType, err)
		}
		parts = multipart.NewReader(message.Body, params["boundary"])
		part, err := parts.NextRawPart() // NextPart silently decodes QP and removes its header.
		if err != nil {
			t.Fatal(err)
		}
		textHeader, textReader = mail.Header(part.Header), part
	}
	if got := textHeader.Get("Content-Transfer-Encoding"); got != "quoted-printable" {
		t.Fatalf("text Content-Transfer-Encoding = %q, want quoted-printable", got)
	}
	if got := textHeader.Get("Content-Type"); got != `text/plain; charset="UTF-8"` {
		t.Fatalf("text Content-Type = %q", got)
	}
	encoded, err := io.ReadAll(textReader)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(encoded, []byte("\r\n")) {
		if len(line) > 76 {
			t.Fatalf("encoded text line is %d bytes, want at most 76", len(line))
		}
		for _, b := range line {
			if b > 127 || b == '\r' || b == '\n' {
				t.Fatalf("non-ASCII or non-CRLF data in encoded line: %q", line)
			}
		}
	}
	decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	// Text MIME canonicalizes line endings; it must preserve every authored break,
	// space and Unicode character, without adding hard wraps or joining lines.
	want := strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	if string(decoded) != want {
		t.Fatalf("decoded body changed:\ngot  %q\nwant %q", decoded, want)
	}
	for _, attachment := range attachments {
		part, err := parts.NextRawPart()
		if err != nil {
			t.Fatal(err)
		}
		if part.FileName() != attachment.Filename || part.Header.Get("Content-Transfer-Encoding") != "base64" {
			t.Fatalf("attachment headers changed: %v", part.Header)
		}
		data, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
		if err != nil || !bytes.Equal(data, attachment.Data) {
			t.Fatalf("attachment changed: %q, %v", data, err)
		}
	}
	if parts != nil {
		if _, err := parts.NextRawPart(); err != io.EOF {
			t.Fatalf("extra MIME part or broken closing boundary: %v", err)
		}
	}
	return message
}

func TestMessageTextEncoding(t *testing.T) {
	bodies := map[string]string{
		"long paragraphs":     strings.Repeat("A paragraph that should flow naturally at any window width. ", 40) + "\n\n" + strings.Repeat("Second paragraph. ", 30),
		"unicode":             strings.Repeat("Café — résumé 日本語 🙂 = yes. ", 50),
		"structure":           "Hi Tyler,\n\n- first item\n- second item\n\n> quoted text\n\nThanks,\nVi\n",
		"CRLF":                "First paragraph.\r\n\r\nSecond paragraph.\r\n",
		"whitespace":          "Leading and trailing whitespace: \t\n\tindented\n\n\n",
		"empty":               "",
		"soft-break boundary": strings.Repeat("a", 75) + "=🙂" + strings.Repeat("b", 80),
	}
	for name, body := range bodies {
		for _, withAttachment := range []bool{false, true} {
			mode := "plain"
			var attachments []Attachment
			if withAttachment {
				mode = "attachment"
				attachments = []Attachment{{Filename: "test.bin", MimeType: "application/octet-stream", Data: []byte{0, 1, 2, 255}}}
			}
			t.Run(name+"/"+mode, func(t *testing.T) {
				assertTextEncoding(t, createMessage("test@example.com", "Body encoding", body, attachments), body, attachments)
			})
		}
	}
}

func TestSendAndReplyPreserveParagraphsOnWire(t *testing.T) {
	for _, reply := range []bool{false, true} {
		for _, withAttachment := range []bool{false, true} {
			name := "send"
			if reply {
				name = "reply"
			}
			if withAttachment {
				name += "/attachment"
			}
			t.Run(name, func(t *testing.T) {
				captured := make(chan *gmail.Message, 1)
				s := &Service{svc: fakeGmailUsersService(t, func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == "GET" && strings.Contains(r.URL.Path, "/threads/thread-1"):
						writeJSON(t, w, threadWithMessage("thread-1"))
					case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/messages/send"):
						var msg gmail.Message
						if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
							t.Errorf("decode sent message: %v", err)
							http.Error(w, "bad message", 400)
							return
						}
						captured <- &msg
						writeJSON(t, w, &gmail.Message{Id: "sent-1", ThreadId: "thread-1"})
					default:
						t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
						http.NotFound(w, r)
					}
				})}
				body := strings.Repeat("Unbroken UTF-8 paragraph — keep flowing. ", 40) + "\n\nThanks,\nVi"
				var attachments []Attachment
				if withAttachment {
					attachments = []Attachment{{Filename: "note.txt", Data: []byte("unchanged attachment")}}
				}
				if reply {
					result, err := s.ReplyToEmail(context.Background(), "thread-1", "", "", body, attachments)
					if err != nil {
						t.Fatal(err)
					}
					if !result.Threaded {
						t.Fatal("reply lost its thread")
					}
				} else {
					if _, err := s.SendEmail(context.Background(), "test@example.com", "Body encoding", body, attachments); err != nil {
						t.Fatal(err)
					}
				}
				select {
				case msg := <-captured:
					message := assertTextEncoding(t, msg, body, attachments)
					if reply && (msg.ThreadId != "thread-1" || message.Header.Get("In-Reply-To") != "<m1@mail>" || message.Header.Get("References") != "<m1@mail>") {
						t.Fatalf("reply metadata changed: %s, %v", msg.ThreadId, message.Header)
					}
				default:
					t.Fatal("no message was sent")
				}
			})
		}
	}
}
