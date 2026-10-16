package gmail

import (
	"context"
	"encoding/base64"
	gmailapi "google.golang.org/api/gmail/v1"
	"net/http"
	"strings"
	"testing"
)

func TestReadThreadUsesOneFullResponseInOrder(t *testing.T) {
	calls := 0
	s := &Service{svc: fakeGmailUsersService(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasSuffix(r.URL.Path, "/threads/t") || r.URL.Query().Get("format") != "full" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		messages := []*gmailapi.Message{}
		for _, id := range []string{"m2", "m1"} {
			messages = append(messages, &gmailapi.Message{Id: id, InternalDate: map[string]int64{"m1": 1, "m2": 2}[id], ThreadId: "t", Payload: &gmailapi.MessagePart{MimeType: "text/plain", Headers: []*gmailapi.MessagePartHeader{{Name: "From", Value: "sender@example.test"}}, Body: &gmailapi.MessagePartBody{Data: base64.RawURLEncoding.EncodeToString([]byte(id + " body"))}}})
		}
		writeJSON(t, w, &gmailapi.Thread{Id: "t", Messages: messages})
	})}
	messages, err := s.GetThread(context.Background(), "t")
	if err != nil || calls != 1 || len(messages) != 2 || messages[0].ID != "m1" || messages[1].Body != "m2 body" || messages[1].From != "sender@example.test" {
		t.Fatalf("thread: %+v %v calls %d", messages, err, calls)
	}
}
