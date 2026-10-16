package gmail

import (
	"context"
	gmailapi "google.golang.org/api/gmail/v1"
	"net/http"
	"strings"
	"testing"
)

func TestThreadIDsUseMetadataOnlyInOrder(t *testing.T) {
	calls := 0
	s := &Service{svc: fakeGmailUsersService(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasSuffix(r.URL.Path, "/threads/t") || r.URL.Query().Get("format") != "metadata" || r.URL.Query().Get("fields") != "id,messages(id,threadId,internalDate)" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		messages := []*gmailapi.Message{}
		for _, id := range []string{"m2", "m1", "m0"} {
			messages = append(messages, &gmailapi.Message{Id: id, InternalDate: map[string]int64{"m0": 1, "m1": 1, "m2": 2}[id], ThreadId: "t"})
		}
		writeJSON(t, w, &gmailapi.Thread{Id: "t", Messages: messages})
	})}
	ids, err := s.GetThreadMessageIDs(context.Background(), "t")
	if err != nil || calls != 1 || strings.Join(ids, ",") != "m0,m1,m2" {
		t.Fatalf("thread IDs: %+v %v calls %d", ids, err, calls)
	}
}
