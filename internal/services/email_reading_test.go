package services

import (
	"github.com/sombi/pi-google-services/internal/gmail"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHTMLMailCleanup(t *testing.T) {
	source := `<html><head><style>.huge {color:red}</style></head><body><script>stealSecrets()</script><p>Hello &amp; <b>world</b>!</p><p>Caf&#233;&nbsp;&#128578;<br>Next line</p><div>hel<b>lo</b></div></body></html>`
	got := stripHTML(source)
	for _, want := range []string{"Hello & world!", "Caf\u00e9 \U0001f642", "Next line", "hello"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "stealSecrets") || strings.Contains(got, "color:red") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "\n") {
		t.Fatal("block boundaries lost")
	}
	if got := stripHTML("<p>Malformed &lt; content<br>still here"); !strings.Contains(got, "Malformed < content\nstill here") {
		t.Fatal(got)
	}
}
func TestEmailPagesPreserveEveryCharacter(t *testing.T) {
	body := strings.Repeat("\U0001f642\u00e9", 2501) + "\n\n  authored line\n"
	detail := &gmail.EmailDetail{EmailSummary: gmail.EmailSummary{ID: "m", ThreadID: "t"}, Body: body, Attachments: []gmail.AttachmentInfo{{AttachmentID: "a", Filename: "file.txt"}}}
	first, err := emailPage(detail, 0)
	if err != nil || first.NextOffset == nil || *first.NextOffset != 5000 || !utf8.ValidString(first.Body) {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := emailPage(detail, *first.NextOffset)
	if err != nil || second.NextOffset != nil || first.Body+second.Body != body || len(second.Attachments) != 1 {
		t.Fatalf("continuation: %+v %v", second, err)
	}
	for _, offset := range []int{-1, len([]rune(body)) + 1} {
		if _, err := emailPage(detail, offset); err == nil {
			t.Fatal("invalid offset accepted")
		}
	}
}

func TestHTMLImplicitHeadAndLayout(t *testing.T) {
	if got := stripHTML("<html><head><title>hidden title</title><style>hidden style</style><body><p>Hello</p></body></html>"); got != "Hello" {
		t.Fatalf("implicit head: %q", got)
	}
	got := stripHTML("<pre>first\n  indented\nlast</pre><table><tr><td>Total</td><td>$45</td></tr></table><h6>Heading</h6>")
	if !strings.Contains(got, "first\n  indented\nlast") || strings.Contains(got, "Total$45") || !strings.Contains(got, "$45\n") {
		t.Fatalf("layout: %q", got)
	}
}
