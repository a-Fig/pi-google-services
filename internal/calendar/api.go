// Package calendar wraps the Google Calendar v3 API.
package calendar

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2"
	gcal "google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
)

// Service wraps the Google Calendar API client.
type Service struct {
	svc *gcal.Service
}

// EventSummary is a simplified calendar event for display.
type EventSummary struct {
	ID         string `json:"id"`
	CalendarID string `json:"calendar_id"`
	// CalendarName is set only when the events were read across calendars (all.go).
	CalendarName string `json:"calendar_name,omitempty"`
	ColorID      string `json:"color_id,omitempty"`
	Summary      string `json:"summary"`
	Description  string `json:"description,omitempty"`
	Start        string `json:"start"`
	End          string `json:"end"`
	Location     string `json:"location,omitempty"`
	HTMLLink     string `json:"html_link,omitempty"`
	Attendees    int    `json:"attendees,omitempty"`
	Creator      string `json:"creator,omitempty"`

	// startAt orders events from different calendars; Start is already formatted for display.
	startAt time.Time
}

// New creates a Service from an OAuth2 token source.
func New(ctx context.Context, ts oauth2.TokenSource) (*Service, error) {
	svc, err := gcal.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("create calendar service: %w", err)
	}
	return &Service{svc: svc}, nil
}

// ListEvents returns events in a time range: on one calendar, or with no calendarID on every
// calendar the account can see (all.go says why that is not just "primary").
func (s *Service) ListEvents(ctx context.Context, calendarID string, timeMin, timeMax time.Time, maxResults int64) ([]*EventSummary, error) {
	if maxResults <= 0 {
		maxResults = 50
	}
	if calendarID == "" {
		return s.everywhere(ctx, maxResults, func(id string) ([]*EventSummary, error) {
			return s.ListEvents(ctx, id, timeMin, timeMax, maxResults)
		})
	}

	events, err := s.svc.Events.List(calendarID).
		TimeMin(timeMin.Format(time.RFC3339)).
		TimeMax(timeMax.Format(time.RFC3339)).
		MaxResults(maxResults).
		OrderBy("startTime").
		SingleEvents(true).
		Do()
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}

	result := make([]*EventSummary, 0, len(events.Items))
	for _, e := range events.Items {
		se := &EventSummary{
			ID:         e.Id,
			CalendarID: calendarID,
			ColorID:    e.ColorId,
			Summary:    e.Summary,
			Start:      fmtDateTime(e.Start),
			End:        fmtDateTime(e.End),
			HTMLLink:   e.HtmlLink,
			startAt:    startOf(e.Start),
		}
		if e.Description != "" {
			se.Description = truncate(e.Description, 200)
		}
		if e.Location != "" {
			se.Location = e.Location
		}
		if len(e.Attendees) > 0 {
			se.Attendees = len(e.Attendees)
		}
		if e.Creator != nil {
			se.Creator = e.Creator.Email
		}
		result = append(result, se)
	}
	return result, nil
}

// CreateEvent creates a new event. If withMeet is true, attaches a Google Meet link.
func (s *Service) CreateEvent(ctx context.Context, calendarID string, event *gcal.Event, withMeet bool) (*gcal.Event, error) {
	if calendarID == "" {
		calendarID = "primary"
	}
	call := s.svc.Events.Insert(calendarID, event)
	if withMeet {
		call.ConferenceDataVersion(1)
		event.ConferenceData = &gcal.ConferenceData{
			CreateRequest: &gcal.CreateConferenceRequest{
				RequestId: fmt.Sprintf("pi-google-%d", time.Now().UnixNano()),
				ConferenceSolutionKey: &gcal.ConferenceSolutionKey{
					Type: "hangoutsMeet",
				},
			},
		}
	}
	created, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("create event: %w", err)
	}
	return created, nil
}

// UpdateEvent patches an existing event, preserving fields that were not supplied.
func (s *Service) UpdateEvent(ctx context.Context, calendarID, eventID string, event *gcal.Event) (*gcal.Event, error) {
	if calendarID == "" {
		calendarID = "primary"
	}
	updated, err := s.svc.Events.Patch(calendarID, eventID, event).Do()
	if err != nil {
		return nil, fmt.Errorf("update event: %w", err)
	}
	return updated, nil
}

// DeleteEvent removes an event.
func (s *Service) DeleteEvent(ctx context.Context, calendarID, eventID string) error {
	if calendarID == "" {
		calendarID = "primary"
	}
	return s.svc.Events.Delete(calendarID, eventID).Do()
}

// SearchEvents queries events by text: on one calendar, or with no calendarID on every calendar
// the account can see.
func (s *Service) SearchEvents(ctx context.Context, calendarID, query string, maxResults int64) ([]*EventSummary, error) {
	if maxResults <= 0 {
		maxResults = 50
	}
	if calendarID == "" {
		return s.everywhere(ctx, maxResults, func(id string) ([]*EventSummary, error) {
			return s.SearchEvents(ctx, id, query, maxResults)
		})
	}
	events, err := s.svc.Events.List(calendarID).
		Q(query).
		MaxResults(maxResults).
		OrderBy("startTime").
		SingleEvents(true).
		Do()
	if err != nil {
		return nil, fmt.Errorf("search events: %w", err)
	}
	result := make([]*EventSummary, 0, len(events.Items))
	for _, e := range events.Items {
		result = append(result, &EventSummary{
			ID:         e.Id,
			CalendarID: calendarID,
			ColorID:    e.ColorId,
			Summary:    e.Summary,
			Start:      fmtDateTime(e.Start),
			End:        fmtDateTime(e.End),
			HTMLLink:   e.HtmlLink,
			startAt:    startOf(e.Start),
		})
	}
	return result, nil
}

// ListCalendars returns all calendars.
func (s *Service) ListCalendars(ctx context.Context) ([]*gcal.CalendarListEntry, error) {
	var calendars []*gcal.CalendarListEntry
	err := s.svc.CalendarList.List().Pages(ctx, func(page *gcal.CalendarList) error {
		calendars = append(calendars, page.Items...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list calendars: %w", err)
	}
	return calendars, nil
}

// GetFreeBusy checks availability: on the calendars named, or with none named on every calendar
// the account can see.
func (s *Service) GetFreeBusy(ctx context.Context, calendarIDs []string, timeMin, timeMax time.Time) (map[string]gcal.FreeBusyCalendar, error) {
	if len(calendarIDs) == 0 {
		calendars, err := s.readable(ctx)
		if err != nil {
			return nil, err
		}
		for _, c := range calendars {
			calendarIDs = append(calendarIDs, c.Id)
		}
	}
	result := make(map[string]gcal.FreeBusyCalendar)
	// Google permits at most 50 calendars in one free/busy request. Pagination
	// can discover more than that, so query bounded batches and combine them.
	for start := 0; start < len(calendarIDs); start += 50 {
		end := min(start+50, len(calendarIDs))
		req := &gcal.FreeBusyRequest{
			TimeMin: timeMin.Format(time.RFC3339),
			TimeMax: timeMax.Format(time.RFC3339),
		}
		for _, id := range calendarIDs[start:end] {
			req.Items = append(req.Items, &gcal.FreeBusyRequestItem{Id: id})
		}
		resp, err := s.svc.Freebusy.Query(req).Do()
		if err != nil {
			return nil, fmt.Errorf("freebusy: %w", err)
		}
		// A per-calendar API error is not an empty (free) schedule. Report all
		// failures in this batch in stable order so the caller can narrow its query.
		var failures []string
		for id, cal := range resp.Calendars {
			if len(cal.Errors) > 0 {
				var reasons []string
				for _, apiErr := range cal.Errors {
					reason := "unknown error"
					if apiErr != nil && apiErr.Reason != "" {
						reason = apiErr.Reason
					}
					reasons = append(reasons, reason)
				}
				failures = append(failures, fmt.Sprintf("calendar %q: %s", id, strings.Join(reasons, ", ")))
			}
			result[id] = cal
		}
		if len(failures) > 0 {
			sort.Strings(failures)
			return nil, fmt.Errorf("freebusy: %s", strings.Join(failures, "; "))
		}
	}
	return result, nil
}

func fmtDateTime(dt *gcal.EventDateTime) string {
	if dt == nil {
		return ""
	}
	if dt.DateTime != "" {
		t, err := time.Parse(time.RFC3339, dt.DateTime)
		if err != nil {
			return dt.DateTime
		}
		return t.Format("Mon Jan 2 15:04 MST")
	}
	if dt.Date != "" {
		return dt.Date
	}
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
