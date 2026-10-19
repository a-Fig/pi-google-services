package gmail

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/api/gmail/v1"
)

// Gmail ANDs labelIds with q, so a search that always carried INBOX could never find mail that
// was sent or archived. SearchEmails must send the query alone; ListInbox keeps the label.
func TestSearchEmailsReachesTheWholeMailboxButListInboxStaysInInbox(t *testing.T) {
	for _, tc := range []struct {
		name      string
		call      func(*Service) ([]*EmailSummary, error)
		wantLabel []string
	}{
		{"SearchEmails", func(s *Service) ([]*EmailSummary, error) {
			return s.SearchEmails(context.Background(), "in:sent newer_than:1d", 5)
		}, nil},
		{"ListInbox", func(s *Service) ([]*EmailSummary, error) {
			return s.ListInbox(context.Background(), 5, "in:sent newer_than:1d")
		}, []string{"INBOX"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{svc: fakeGmailUsersService(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/messages") {
					q := r.URL.Query()
					if got := q["labelIds"]; strings.Join(got, ",") != strings.Join(tc.wantLabel, ",") {
						t.Errorf("labelIds = %v, want %v", got, tc.wantLabel)
					}
					if got := q.Get("q"); got != "in:sent newer_than:1d" {
						t.Errorf("q = %q, want the query passed through", got)
					}
					writeJSON(t, w, &gmail.ListMessagesResponse{Messages: []*gmail.Message{{Id: "m1"}}})
					return
				}
				writeJSON(t, w, &gmail.Message{
					Id:      "m1",
					Payload: &gmail.MessagePart{Headers: []*gmail.MessagePartHeader{{Name: "Subject", Value: "Re: Dinner"}}},
				})
			})}

			got, err := tc.call(s)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if len(got) != 1 || got[0].Subject != "Re: Dinner" {
				t.Fatalf("%s = %+v, want the one message", tc.name, got)
			}
		})
	}
}
