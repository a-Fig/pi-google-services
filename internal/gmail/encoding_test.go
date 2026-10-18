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

	"golang.org/x/net/html"
	"google.golang.org/api/gmail/v1"
)

type mimeHeader interface{ Get(string) string }

// assertTextEncoding decodes the actual RFC 5322 wire message and checks both
// alternatives of its body against the text the caller wrote: the text/plain
// part decodes to exactly that text, and the text/html part renders to it.
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
	var bodyHeader mimeHeader = message.Header
	var bodyReader io.Reader = message.Body
	var mixed *multipart.Reader
	if len(attachments) > 0 {
		mixed = multipart.NewReader(message.Body, boundaryOf(t, message.Header, "multipart/mixed"))
		part, err := mixed.NextRawPart()
		if err != nil {
			t.Fatal(err)
		}
		bodyHeader, bodyReader = part.Header, part
	}
	alternatives := multipart.NewReader(bodyReader, boundaryOf(t, bodyHeader, "multipart/alternative"))

	// Text MIME canonicalizes line endings; the plain part must keep every
	// authored break, space and Unicode character, without adding hard wraps
	// or joining lines. So CRLF appears exactly where the input had a newline.
	text := decodeQuotedPrintablePart(t, alternatives, `text/plain; charset="UTF-8"`)
	if want := strings.ReplaceAll(normalizeNewlines(body), "\n", "\r\n"); text != want {
		t.Fatalf("decoded text/plain changed:\ngot  %q\nwant %q", text, want)
	}
	source := decodeQuotedPrintablePart(t, alternatives, `text/html; charset="UTF-8"`)
	if got, want := renderedText(t, source), normalizeNewlines(body); got != want {
		t.Fatalf("text/html renders differently:\ngot  %q\nwant %q\nsource %q", got, want, source)
	}
	if _, err := alternatives.NextRawPart(); err != io.EOF {
		t.Fatalf("extra alternative or broken closing boundary: %v", err)
	}

	for _, attachment := range attachments {
		part, err := mixed.NextRawPart()
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
	if mixed != nil {
		if _, err := mixed.NextRawPart(); err != io.EOF {
			t.Fatalf("extra MIME part or broken closing boundary: %v", err)
		}
	}
	return message
}

func boundaryOf(t *testing.T, header mimeHeader, want string) string {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || mediaType != want || params["boundary"] == "" {
		t.Fatalf("Content-Type = %q, want %s with a boundary: %v", header.Get("Content-Type"), want, err)
	}
	return params["boundary"]
}

// decodeQuotedPrintablePart reads the next part raw (NextPart would silently
// decode quoted-printable and remove its header), checks its transport lines
// and returns it decoded.
func decodeQuotedPrintablePart(t *testing.T, r *multipart.Reader, contentType string) string {
	t.Helper()
	part, err := r.NextRawPart()
	if err != nil {
		t.Fatalf("missing %s part: %v", contentType, err)
	}
	if got := part.Header.Get("Content-Type"); got != contentType {
		t.Fatalf("Content-Type = %q, want %q", got, contentType)
	}
	if got := part.Header.Get("Content-Transfer-Encoding"); got != "quoted-printable" {
		t.Fatalf("%s Content-Transfer-Encoding = %q, want quoted-printable", contentType, got)
	}
	encoded, err := io.ReadAll(part)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(encoded, []byte("\r\n")) {
		if len(line) > 76 {
			t.Fatalf("encoded %s line is %d bytes, want at most 76", contentType, len(line))
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
	return string(decoded)
}

func normalizeNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// renderedText is the text a mail client displays for htmlBody's output: one
// line break per <br>, entities decoded, and the source line breaks htmlBody
// writes after each <br> dropped, since HTML does not display them. It fails
// on a run of two collapsible spaces, which a client would show as one.
func renderedText(t *testing.T, source string) string {
	t.Helper()
	z := html.NewTokenizer(strings.NewReader(source))
	var out strings.Builder
	for {
		switch z.Next() {
		case html.ErrorToken:
			if z.Err() != io.EOF {
				t.Fatal(z.Err())
			}
			return out.String()
		case html.TextToken:
			text := strings.ReplaceAll(string(z.Raw()), "\r\n", "")
			if strings.Contains(text, "  ") {
				t.Fatalf("HTML text %q would collapse its spaces", text)
			}
			out.WriteString(html.UnescapeString(strings.ReplaceAll(text, "&nbsp;", " ")))
		case html.StartTagToken, html.SelfClosingTagToken:
			if name, _ := z.TagName(); string(name) == "br" {
				out.WriteString("\n")
			}
		}
	}
}

func TestMessageTextEncoding(t *testing.T) {
	bodies := map[string]string{
		"long paragraphs":     strings.Repeat("A paragraph that should flow naturally at any window width. ", 40) + "\n\n" + strings.Repeat("Second paragraph. ", 30),
		"unicode":             strings.Repeat("Café — résumé 日本語 🙂 = yes. ", 50),
		"structure":           "Hi Tyler,\n\n- first item\n- second item\n\n> quoted text\n\nThanks,\nVi\n",
		"CRLF":                "First paragraph.\r\n\r\nSecond paragraph.\r\n",
		"lone CR":             "Old Mac line.\rNext line.",
		"whitespace":          "Leading and trailing whitespace: \t\n\tindented\n\n\n",
		"spaces":              "Two  spaces,   three, and    a literal no-break space.",
		"markup and links":    `Book it at https://example.com/book?when=fri&who=2. Fees (https://en.wikipedia.org/wiki/Fee_(disambiguation)) are <b>not</b> "bold" & won't be.`,
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

// The authored paragraph must be one unbroken run of HTML text: that is what
// lets it wrap to the reader's window instead of at Gmail's 70 columns.
func TestHTMLBody(t *testing.T) {
	paragraph := strings.Repeat("One long paragraph that must stay one line. ", 20)
	cases := map[string]struct{ in, want string }{
		"paragraphs": {
			paragraph + "\n\nThanks,\nVi",
			`<div dir="auto">` + paragraph + "<br>\r\n<br>\r\nThanks,<br>\r\nVi</div>\r\n",
		},
		"escaping": {
			`<script>alert("x")</script> & 'quotes'`,
			`<div dir="auto">&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt; &amp; &#39;quotes&#39;</div>` + "\r\n",
		},
		"link with query and sentence period": {
			"See https://example.com/a?b=1&c=2.",
			`<div dir="auto">See <a href="https://example.com/a?b=1&amp;c=2">https://example.com/a?b=1&amp;c=2</a>.</div>` + "\r\n",
		},
		"link inside parentheses keeps its own": {
			"(https://en.wikipedia.org/wiki/Foo_(bar))",
			`<div dir="auto">(<a href="https://en.wikipedia.org/wiki/Foo_(bar)">https://en.wikipedia.org/wiki/Foo_(bar)</a>)</div>` + "\r\n",
		},
		"link ends at an em dash": {
			"http://example.com—thanks",
			`<div dir="auto"><a href="http://example.com">http://example.com</a>—thanks</div>` + "\r\n",
		},
		"angle-bracketed link": {
			"<https://example.com/x>",
			`<div dir="auto">&lt;<a href="https://example.com/x">https://example.com/x</a>&gt;</div>` + "\r\n",
		},
		"not links": {
			"javascript:alert(1) xhttps://example.com https:// ftp://example.com",
			`<div dir="auto">javascript:alert(1) xhttps://example.com https:// ftp://example.com</div>` + "\r\n",
		},
		"spaces": {
			"a  b   c",
			`<div dir="auto">a &nbsp;b &nbsp; c</div>` + "\r\n",
		},
		"empty": {"", `<div dir="auto"></div>` + "\r\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := htmlBody(tc.in); got != tc.want {
				t.Fatalf("htmlBody(%q)\ngot  %q\nwant %q", tc.in, got, tc.want)
			}
		})
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
