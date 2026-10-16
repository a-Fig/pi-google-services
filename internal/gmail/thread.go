package gmail

import (
	"context"
	"fmt"
	"sort"
)

// GetThread returns Gmail's ordered full conversation from one API response.
func (s *Service) GetThread(ctx context.Context, id string) ([]*EmailDetail, error) {
	thread, err := s.svc.Threads.Get("me", id).Format("full").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("get thread: %w", err)
	}
	sort.Slice(thread.Messages, func(i, j int) bool {
		if thread.Messages[i].InternalDate == thread.Messages[j].InternalDate {
			return thread.Messages[i].Id < thread.Messages[j].Id
		}
		return thread.Messages[i].InternalDate < thread.Messages[j].InternalDate
	})
	details := make([]*EmailDetail, 0, len(thread.Messages))
	for _, message := range thread.Messages {
		details = append(details, detailFromMessage(message))
	}
	return details, nil
}
