package gmail

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/api/gmail/v1"
)

// ListInbox fetches each message's metadata in parallel (pi-vi #324) but must still answer in
// the list's own order, skip a message it cannot read, and never run more than
// listFetchConcurrency fetches at once.
func TestListInboxFetchesInParallelAndKeepsOrder(t *testing.T) {
	const total = 30
	const unreadable = "m7"
	var inFlight, peak atomic.Int32

	s := &Service{svc: fakeGmailUsersService(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/messages") {
			if got := r.URL.Query().Get("labelIds"); got != "INBOX" {
				t.Errorf("list labelIds = %q, want INBOX", got)
			}
			list := &gmail.ListMessagesResponse{}
			for i := 0; i < total; i++ {
				list.Messages = append(list.Messages, &gmail.Message{Id: fmt.Sprintf("m%d", i)})
			}
			writeJSON(t, w, list)
			return
		}
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		// Long enough that fetches started together overlap, so a serial loop shows a peak of 1.
		time.Sleep(20 * time.Millisecond)
		if id == unreadable {
			writeGoogleAPIError(t, w, http.StatusNotFound, "Requested entity was not found.")
			return
		}
		writeJSON(t, w, &gmail.Message{
			Id:       id,
			ThreadId: "t-" + id,
			Snippet:  "snippet " + id,
			Payload: &gmail.MessagePart{Headers: []*gmail.MessagePartHeader{
				{Name: "Subject", Value: "subject " + id},
				{Name: "From", Value: "sender@example.com"},
			}},
		})
	})}

	got, err := s.ListInbox(context.Background(), total, "")
	if err != nil {
		t.Fatalf("ListInbox: %v", err)
	}

	var want []string
	for i := 0; i < total; i++ {
		if id := fmt.Sprintf("m%d", i); id != unreadable {
			want = append(want, id)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("got %d summaries, want %d", len(got), len(want))
	}
	for i, summary := range got {
		if summary.ID != want[i] {
			t.Fatalf("summary %d is %s, want %s: order must follow the list", i, summary.ID, want[i])
		}
		if summary.Subject != "subject "+want[i] || summary.ThreadID != "t-"+want[i] || summary.From != "sender@example.com" {
			t.Errorf("summary %d = %+v, fields not read from its own message", i, summary)
		}
	}
	if p := peak.Load(); p < 2 || p > listFetchConcurrency {
		t.Errorf("peak concurrent fetches = %d, want between 2 and %d", p, listFetchConcurrency)
	}
}
