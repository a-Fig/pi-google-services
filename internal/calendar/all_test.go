package calendar

import (
	"testing"

	gcal "google.golang.org/api/calendar/v3"
)

func TestReadableCalendarsKeepsSharedAndUnselected(t *testing.T) {
	got := readableCalendars([]*gcal.CalendarListEntry{
		{Id: "vi@example.com", Summary: "Vi", Primary: true, Selected: true},
		{Id: "school@group.calendar.google.com", Summary: "School", AccessRole: "writer"},
		{Id: "gone@group.calendar.google.com", Summary: "Gone", Deleted: true},
		{Id: "hidden@group.calendar.google.com", Summary: "Hidden", Hidden: true},
		nil,
	})
	if len(got) != 2 || got[0].Id != "vi@example.com" || got[1].Id != "school@group.calendar.google.com" {
		t.Fatalf("readableCalendars() = %+v, want the primary and the shared one", got)
	}
}

func TestCalendarNamePrefersTheAccountsOwnName(t *testing.T) {
	if got := calendarName(&gcal.CalendarListEntry{Summary: "tyler@example.com", SummaryOverride: "Personal"}); got != "Personal" {
		t.Errorf("calendarName() = %q, want the override", got)
	}
	if got := calendarName(&gcal.CalendarListEntry{Summary: "School"}); got != "School" {
		t.Errorf("calendarName() = %q, want the summary", got)
	}
}

func TestMergeByStartInterleavesCalendarsAndCaps(t *testing.T) {
	ev := func(id string, dt *gcal.EventDateTime) *EventSummary {
		return &EventSummary{ID: id, startAt: startOf(dt)}
	}
	// Each list is in start order, as the API returns it; the offsets differ on purpose.
	school := []*EventSummary{
		ev("lecture", &gcal.EventDateTime{DateTime: "2026-10-02T09:00:00-07:00"}),
		ev("lab", &gcal.EventDateTime{DateTime: "2026-10-02T15:00:00-07:00"}),
	}
	personal := []*EventSummary{
		ev("all-day", &gcal.EventDateTime{Date: "2026-10-02"}),
		ev("dentist", &gcal.EventDateTime{DateTime: "2026-10-02T18:30:00Z"}), // 11:30 Pacific
	}

	got := mergeByStart([][]*EventSummary{school, personal}, 3)
	want := []string{"all-day", "lecture", "dentist"}
	if len(got) != len(want) {
		t.Fatalf("mergeByStart() returned %d events, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("mergeByStart()[%d] = %q, want %q", i, got[i].ID, id)
		}
	}
}

func TestMergeByStartOfNothingIsEmptyNotNil(t *testing.T) {
	if got := mergeByStart(nil, 50); got == nil || len(got) != 0 {
		t.Errorf("mergeByStart(nil) = %#v, want an empty list", got)
	}
}
