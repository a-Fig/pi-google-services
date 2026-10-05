package calendar

import (
	"context"
	"fmt"
	"sort"
	"time"

	gcal "google.golang.org/api/calendar/v3"
)

// Reading with no calendar ID.
//
// These used to fall back to "primary", the signed-in account's own calendar. That is the wrong
// answer whenever the account reads calendars shared with it, which is the whole job of an
// assistant's account: the lookup ran on a calendar nobody keeps events on, found nothing, and
// "nothing on the calendar" looked like a real result. So a read with no ID now covers every
// calendar the account can see, and says which calendar each event is on. Writes still default to
// "primary": there is no safe guess for where a new event belongs.

// everywhere runs one read on each readable calendar and merges the results in start order.
// One calendar failing fails the read: a partial list would be the same silent hole again.
func (s *Service) everywhere(ctx context.Context, maxResults int64, read func(calendarID string) ([]*EventSummary, error)) ([]*EventSummary, error) {
	calendars, err := s.readable(ctx)
	if err != nil {
		return nil, err
	}
	lists := make([][]*EventSummary, 0, len(calendars))
	for _, c := range calendars {
		events, err := read(c.Id)
		if err != nil {
			return nil, fmt.Errorf("calendar %q (%s): %w", calendarName(c), c.Id, err)
		}
		for _, e := range events {
			e.CalendarName = calendarName(c)
		}
		lists = append(lists, events)
	}
	return mergeByStart(lists, maxResults), nil
}

// readable lists the calendars a read with no ID covers.
func (s *Service) readable(ctx context.Context) ([]*gcal.CalendarListEntry, error) {
	all, err := s.ListCalendars(ctx)
	if err != nil {
		return nil, err
	}
	return readableCalendars(all), nil
}

// readableCalendars drops what the account has removed or hidden from its own list. Everything
// else counts, selected in the Calendar UI or not: nobody ticks boxes in an assistant's account.
func readableCalendars(all []*gcal.CalendarListEntry) []*gcal.CalendarListEntry {
	kept := make([]*gcal.CalendarListEntry, 0, len(all))
	for _, c := range all {
		if c == nil || c.Deleted || c.Hidden {
			continue
		}
		kept = append(kept, c)
	}
	return kept
}

// calendarName is the name the account sees: its own override if it set one.
func calendarName(c *gcal.CalendarListEntry) string {
	if c.SummaryOverride != "" {
		return c.SummaryOverride
	}
	return c.Summary
}

// mergeByStart interleaves per-calendar lists by start time and keeps the first max.
func mergeByStart(lists [][]*EventSummary, max int64) []*EventSummary {
	merged := make([]*EventSummary, 0)
	for _, l := range lists {
		merged = append(merged, l...)
	}
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].startAt.Before(merged[j].startAt) })
	if max > 0 && int64(len(merged)) > max {
		merged = merged[:max]
	}
	return merged
}

// startOf is when an event starts, for ordering. An all-day event sorts at the top of its day.
func startOf(dt *gcal.EventDateTime) time.Time {
	if dt == nil {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, dt.DateTime); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02", dt.Date); err == nil {
		return t
	}
	return time.Time{}
}
