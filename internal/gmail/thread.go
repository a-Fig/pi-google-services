package gmail

import (
	"context"
	"fmt"
	"sort"
)

// GetThreadMessageIDs validates/order-pages a conversation without downloading any
// bodies. The explicit projection also excludes snippets, headers and MIME payloads.
func (s *Service) GetThreadMessageIDs(ctx context.Context, id string) ([]string, error) {
	thread, err := s.svc.Threads.Get("me", id).Format("metadata").
		Fields("id,messages(id,threadId,internalDate)").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("get thread: %w", err)
	}
	sort.Slice(thread.Messages, func(i, j int) bool {
		if thread.Messages[i].InternalDate == thread.Messages[j].InternalDate {
			return thread.Messages[i].Id < thread.Messages[j].Id
		}
		return thread.Messages[i].InternalDate < thread.Messages[j].InternalDate
	})
	ids := make([]string, 0, len(thread.Messages))
	for _, message := range thread.Messages {
		ids = append(ids, message.Id)
	}
	return ids, nil
}
