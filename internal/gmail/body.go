package gmail

import (
	"bytes"
	"fmt"
	"html"
	"mime/quotedprintable"
	"regexp"
	"strings"
)

// Why every outgoing message carries an HTML part (pi-vi #265).
//
// Gmail does not deliver a text/plain part the way it was submitted. It decodes
// the part and writes it out again folded at word boundaries, at most 70
// columns, with CRLF, no Content-Transfer-Encoding and no format=flowed: the
// same shape as the plain part of mail written in Gmail's own composer. Every
// client shows those folds as line breaks, so a paragraph Vi sent as one line
// arrived broken mid-sentence. Seen on pi-vi #265 for an unencoded part, and
// quoted-printable parts (pi-google-services #3) kept arriving folded too,
// because the folding happens after Gmail has decoded them.
//
// The text/html part of a multipart/alternative message is not folded that
// way, and HTML ignores source line breaks anyway; Gmail and other clients
// display it in preference to the plain part. So the plain text the caller wrote
// goes first, for clients that only read plain text, and a minimal HTML
// rendering of the same text goes last: each authored line break becomes <br>
// and nothing else is added, so paragraphs wrap to the reader's window.

// writeBody writes the message body as a multipart/alternative entity,
// starting with its own Content-Type header.
func writeBody(buf *bytes.Buffer, body, boundary string) {
	fmt.Fprintf(buf, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", boundary)
	fmt.Fprintf(buf, "--%s\r\nContent-Type: text/plain; charset=\"UTF-8\"\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n%s\r\n", boundary, quotedPrintable(body))
	fmt.Fprintf(buf, "--%s\r\nContent-Type: text/html; charset=\"UTF-8\"\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n%s\r\n", boundary, quotedPrintable(htmlBody(body)))
	fmt.Fprintf(buf, "--%s--\r\n", boundary)
}

// quotedPrintable keeps every transport line within 76 characters with soft
// breaks, which decode away, and carries UTF-8 through 7-bit transports. Line
// breaks in s become CRLF.
func quotedPrintable(s string) string {
	var out bytes.Buffer
	w := quotedprintable.NewWriter(&out)
	_, _ = w.Write([]byte(s)) // bytes.Buffer writes cannot fail.
	_ = w.Close()             // Flush the final line before reading out.
	return out.String()
}

// htmlBody renders plain text as HTML that displays the same text: escaped,
// one <br> per line break (CRLF, LF or a lone CR, as the quoted-printable
// writer counts them), runs of spaces kept, and bare http(s) URLs linked.
func htmlBody(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var b strings.Builder
	b.WriteString(`<div dir="auto">`)
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			b.WriteString("<br>\r\n")
		}
		writeHTMLLine(&b, line)
	}
	b.WriteString("</div>\r\n")
	return b.String()
}

// bareURL matches an http(s) URL written as text. It takes only characters a
// URL carries unescaped, so an em dash or curly quote written straight after a
// link stays out of it.
var bareURL = regexp.MustCompile(`(?i)\bhttps?://[a-z0-9\-._~:/?#\[\]@!$&'()*+,;=%]+`)

func writeHTMLLine(b *strings.Builder, line string) {
	last := 0
	for _, m := range bareURL.FindAllStringIndex(line, -1) {
		url := trimURL(line[m[0]:m[1]])
		if strings.HasSuffix(url, "://") {
			continue // A scheme with nothing after it.
		}
		escaped := html.EscapeString(url)
		b.WriteString(htmlText(line[last:m[0]]))
		b.WriteString(`<a href="` + escaped + `">` + escaped + `</a>`)
		last = m[0] + len(url)
	}
	b.WriteString(htmlText(line[last:]))
}

// trimURL drops punctuation that ends the sentence rather than the URL: a
// trailing period or comma, and a closing bracket the URL did not open.
func trimURL(url string) string {
	for url != "" {
		switch c := url[len(url)-1]; {
		case strings.IndexByte(".,;:!?'*", c) >= 0:
		case c == ')' && strings.Count(url, "(") < strings.Count(url, ")"):
		case c == ']' && strings.Count(url, "[") < strings.Count(url, "]"):
		default:
			return url
		}
		url = url[:len(url)-1]
	}
	return url
}

// htmlText escapes text and keeps runs of spaces, which HTML would collapse
// into one.
func htmlText(s string) string {
	return strings.ReplaceAll(html.EscapeString(s), "  ", " &nbsp;")
}
