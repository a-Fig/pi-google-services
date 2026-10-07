package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
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

// A get Gmail turns away for the moment is tried again, and one that still fails takes the whole
// list down with it rather than leaving a hole: email-watch's first poll takes a list as
// everything there is, and would report a message dropped from it as new mail later.
func TestListInboxRetriesRateLimitsAndNeverDropsSilently(t *testing.T) {
	old := fetchRetryDelay
	fetchRetryDelay = time.Millisecond
	t.Cleanup(func() { fetchRetryDelay = old })
	ids := []string{"m0", "m1", "m2", "m3", "m4", "m5"}

	t.Run("rate limits and server errors are tried again", func(t *testing.T) {
		s, attempts := serveInbox(t, ids, func(w http.ResponseWriter, id string, attempt int) bool {
			switch {
			case id == "m1" && attempt == 1:
				w.Header().Set("Retry-After", "0")
				writeGoogleAPIError(t, w, http.StatusTooManyRequests, "Too many concurrent requests for user")
			case id == "m2" && attempt < listFetchAttempts:
				writeGoogleAPIErrorReason(t, w, http.StatusForbidden, "userRateLimitExceeded")
			case id == "m3" && attempt == 1:
				writeGoogleAPIError(t, w, http.StatusServiceUnavailable, "Backend Error")
			case id == "m4":
				writeGoogleAPIError(t, w, http.StatusNotFound, "Requested entity was not found.")
			default:
				return false
			}
			return true
		})
		got, err := s.ListInbox(context.Background(), int64(len(ids)), "")
		if err != nil {
			t.Fatalf("ListInbox: %v", err)
		}
		var gotIDs []string
		for _, summary := range got {
			gotIDs = append(gotIDs, summary.ID)
		}
		if want := "m0 m1 m2 m3 m5"; strings.Join(gotIDs, " ") != want {
			t.Errorf("got %v, want %s: only the message Gmail no longer has may be missing", gotIDs, want)
		}
		for id, want := range map[string]int{"m0": 1, "m1": 2, "m2": listFetchAttempts, "m3": 2, "m4": 1} {
			if n := attempts(id); n != want {
				t.Errorf("%s fetched %d times, want %d", id, n, want)
			}
		}
	})

	t.Run("a message that keeps failing fails the list", func(t *testing.T) {
		s, attempts := serveInbox(t, ids, func(w http.ResponseWriter, id string, attempt int) bool {
			if id != "m2" {
				return false
			}
			writeGoogleAPIError(t, w, http.StatusInternalServerError, "Backend Error")
			return true
		})
		got, err := s.ListInbox(context.Background(), int64(len(ids)), "")
		if err == nil || !strings.Contains(err.Error(), "m2") {
			t.Fatalf("ListInbox = %d summaries, err %v; want an error naming m2", len(got), err)
		}
		if n := attempts("m2"); n != listFetchAttempts {
			t.Errorf("m2 fetched %d times, want %d", n, listFetchAttempts)
		}
	})

	t.Run("other errors are not tried again", func(t *testing.T) {
		s, attempts := serveInbox(t, ids, func(w http.ResponseWriter, id string, attempt int) bool {
			if id != "m2" {
				return false
			}
			writeGoogleAPIErrorReason(t, w, http.StatusForbidden, "insufficientPermissions")
			return true
		})
		if _, err := s.ListInbox(context.Background(), int64(len(ids)), ""); err == nil || !strings.Contains(err.Error(), "m2") {
			t.Fatalf("ListInbox err = %v, want an error naming m2", err)
		}
		if n := attempts("m2"); n != 1 {
			t.Errorf("m2 fetched %d times, want 1", n)
		}
	})
}

// serveInbox fakes a Gmail inbox listing ids. fail may answer a get itself, returning true when it
// did; attempts reports how many gets reached the server for an id.
func serveInbox(t *testing.T, ids []string, fail func(w http.ResponseWriter, id string, attempt int) bool) (*Service, func(id string) int) {
	t.Helper()
	var mu sync.Mutex
	counts := map[string]int{}
	s := &Service{svc: fakeGmailUsersService(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/messages") {
			list := &gmail.ListMessagesResponse{}
			for _, id := range ids {
				list.Messages = append(list.Messages, &gmail.Message{Id: id})
			}
			writeJSON(t, w, list)
			return
		}
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		mu.Lock()
		counts[id]++
		attempt := counts[id]
		mu.Unlock()
		if fail(w, id, attempt) {
			return
		}
		writeJSON(t, w, &gmail.Message{Id: id, ThreadId: "t-" + id, Payload: &gmail.MessagePart{}})
	})}
	return s, func(id string) int {
		mu.Lock()
		defer mu.Unlock()
		return counts[id]
	}
}

// writeGoogleAPIErrorReason is writeGoogleAPIError with the reason Gmail gives in its errors
// list, which is what tells a rate-limit 403 from a permission one.
func writeGoogleAPIErrorReason(t *testing.T, w http.ResponseWriter, code int, reason string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": reason,
			"errors":  []map[string]string{{"domain": "usageLimits", "reason": reason, "message": reason}},
		},
	}); err != nil {
		t.Errorf("encode error body: %v", err)
	}
}
