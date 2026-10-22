package gmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/mail"
	"strings"
	"testing"

	"google.golang.org/api/gmail/v1"
)

func addresses(t *testing.T, list string) []*mail.Address {
	t.Helper()
	parsed, err := ParseRecipients(list)
	if err != nil {
		t.Fatalf("ParseRecipients(%q): %v", list, err)
	}
	return parsed
}

func bare(list []*mail.Address) string {
	return strings.Join(addressesOf(list), ",")
}

func TestParseRecipients(t *testing.T) {
	got := addresses(t, "a@x.com, Tyler D <T@Y.com>")
	if bare(got) != "a@x.com,T@Y.com" || got[1].Name != "Tyler D" {
		t.Errorf("parsed %+v", got)
	}
	if got := addresses(t, "  "); got != nil {
		t.Errorf("blank list = %+v, want none", got)
	}
	for _, bad := range []string{"not an address", "a@x.com\r\nBcc: spy@z.com", "a@x.com\nX: y"} {
		if _, err := ParseRecipients(bad); err == nil {
			t.Errorf("ParseRecipients(%q) accepted it", bad)
		}
	}
}

func TestCopyHeaders(t *testing.T) {
	if got := copyHeaders(nil, nil); got != "" {
		t.Errorf("no copies wrote %q", got)
	}
	got := copyHeaders(addresses(t, "a@x.com, José <j@y.com>"), addresses(t, "hidden@z.com"))
	if !strings.HasPrefix(got, "Cc: <a@x.com>,\r\n =?utf-8?q?Jos=C3=A9?= <j@y.com>\r\n") {
		t.Errorf("Cc header = %q", got)
	}
	if !strings.HasSuffix(got, "Bcc: <hidden@z.com>\r\n") {
		t.Errorf("Bcc header = %q", got)
	}
}

func TestLenientAddressesKeepsWhatParses(t *testing.T) {
	got := lenientAddresses("good@x.com, <<broken, Name <ok@y.com>")
	if bare(got) != "good@x.com,ok@y.com" {
		t.Errorf("lenient parse = %s", bare(got))
	}
}

func TestReplyAllCc(t *testing.T) {
	recipients := addresses(t, "Me <ME@vi.com>, friend@x.com, Sender <sender@x.com>, extra@x.com, secret@x.com, friend@x.com")
	got := replyAllCc(recipients, addresses(t, "owner@x.com"), addresses(t, "secret@x.com"), "Sender <sender@x.com>", "me@vi.com")
	// The explicit copy first; then the thread's other recipients, less this
	// account, the reply's To, the Bcc and the repeat.
	if bare(got) != "owner@x.com,friend@x.com,extra@x.com" {
		t.Errorf("reply-all Cc = %s", bare(got))
	}
}

func TestExtractReplyTargetCollectsRecipients(t *testing.T) {
	target := extractReplyTarget(&gmail.Message{Payload: &gmail.MessagePart{Headers: []*gmail.MessagePartHeader{
		header("From", "sender@x.com"),
		header("To", "vi@x.com, owner@x.com"),
		header("Cc", "Third <third@x.com>"),
	}}})
	if bare(target.Recipients) != "vi@x.com,owner@x.com,third@x.com" {
		t.Errorf("recipients = %s", bare(target.Recipients))
	}
}

// sentMessage serves one thread and captures what is sent, as
// TestSendAndReplyPreserveParagraphsOnWire does, plus the profile a reply-all reads.
func sentMessage(t *testing.T, thread *gmail.Thread, send func(*Service)) *mail.Message {
	t.Helper()
	captured := make(chan *gmail.Message, 1)
	s := &Service{svc: fakeGmailUsersService(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/profile"):
			writeJSON(t, w, &gmail.Profile{EmailAddress: "vi@x.com"})
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/threads/thread-1"):
			writeJSON(t, w, thread)
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
	send(s)
	select {
	case msg := <-captured:
		parsed, err := mail.ReadMessage(strings.NewReader(decodeRaw(t, msg)))
		if err != nil {
			t.Fatalf("parse sent message: %v", err)
		}
		return parsed
	default:
		t.Fatal("no message was sent")
		return nil
	}
}

func TestSendEmailCarriesCopies(t *testing.T) {
	msg := sentMessage(t, nil, func(s *Service) {
		copies := Copies{Cc: addresses(t, "owner@x.com"), Bcc: addresses(t, "quiet@x.com")}
		if _, err := s.SendEmail(context.Background(), "jared@x.com", "Hi", "body", copies, nil); err != nil {
			t.Fatal(err)
		}
	})
	if msg.Header.Get("To") != "jared@x.com" || msg.Header.Get("Cc") != "<owner@x.com>" || msg.Header.Get("Bcc") != "<quiet@x.com>" {
		t.Errorf("headers = %v", msg.Header)
	}
}

func TestReplyAllCopiesTheThread(t *testing.T) {
	thread := &gmail.Thread{Id: "thread-1", Messages: []*gmail.Message{
		{Payload: &gmail.MessagePart{Headers: []*gmail.MessagePartHeader{
			header("Message-ID", "<m1@mail>"),
			header("Subject", "Cal Hacks"),
			header("From", "Jared <jared@x.com>"),
			header("To", "Vi <vi@x.com>"),
			header("Cc", "owner@x.com, jared@x.com"),
		}}},
	}}
	var result *ReplyResult
	msg := sentMessage(t, thread, func(s *Service) {
		var err error
		result, err = s.ReplyToEmail(context.Background(), "thread-1", "", "", "Sounds good", Copies{ReplyAll: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
	})
	if msg.Header.Get("To") != "Jared <jared@x.com>" || msg.Header.Get("Cc") != "<owner@x.com>" {
		t.Errorf("headers = %v", msg.Header)
	}
	if bare(result.Cc) != "owner@x.com" || msg.Header.Get("In-Reply-To") != "<m1@mail>" {
		t.Errorf("result Cc = %s, In-Reply-To = %q", bare(result.Cc), msg.Header.Get("In-Reply-To"))
	}
}

func TestReplyWithoutReplyAllCopiesOnlyWhatIsAsked(t *testing.T) {
	thread := &gmail.Thread{Id: "thread-1", Messages: []*gmail.Message{
		{Payload: &gmail.MessagePart{Headers: []*gmail.MessagePartHeader{
			header("From", "jared@x.com"),
			header("Cc", "someone@x.com"),
		}}},
	}}
	msg := sentMessage(t, thread, func(s *Service) {
		if _, err := s.ReplyToEmail(context.Background(), "thread-1", "", "", "ok", Copies{Cc: addresses(t, "owner@x.com")}, nil); err != nil {
			t.Fatal(err)
		}
	})
	if msg.Header.Get("Cc") != "<owner@x.com>" {
		t.Errorf("Cc = %q", msg.Header.Get("Cc"))
	}
}
