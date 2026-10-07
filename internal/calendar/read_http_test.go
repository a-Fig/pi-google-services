package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gcal "google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
)

func calendarFixture(t *testing.T, explicit bool, sharedFailure bool) *Service {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		write := func(v interface{}) {
			if err := json.NewEncoder(w).Encode(v); err != nil {
				t.Error(err)
			}
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/users/me/calendarList"):
			if explicit {
				t.Error("explicit reads must not enumerate other calendars")
			}
			if r.URL.Query().Get("pageToken") == "shared-page" {
				write(&gcal.CalendarList{Items: []*gcal.CalendarListEntry{{Id: "school", Summary: "Owner School", AccessRole: "reader"}}})
			} else {
				write(&gcal.CalendarList{NextPageToken: "shared-page", Items: []*gcal.CalendarListEntry{{Id: "vi", Summary: "Vi", Primary: true}}})
			}
		case strings.HasSuffix(r.URL.Path, "/calendars/vi/events"), strings.HasSuffix(r.URL.Path, "/calendars/primary/events"):
			write(&gcal.Events{})
		case strings.HasSuffix(r.URL.Path, "/calendars/school/events"):
			if sharedFailure {
				w.WriteHeader(403)
				write(map[string]interface{}{"error": map[string]interface{}{"code": 403, "message": "access revoked"}})
				return
			}
			write(&gcal.Events{Items: []*gcal.Event{{Id: "lab", Summary: "Biology lab", Start: &gcal.EventDateTime{DateTime: "2026-10-06T09:00:00-07:00"}}}})
		case strings.HasSuffix(r.URL.Path, "/freeBusy"):
			var req gcal.FreeBusyRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			ids := []string{}
			for _, item := range req.Items {
				ids = append(ids, item.Id)
			}
			want := "vi,school"
			if explicit {
				want = "primary"
			}
			if strings.Join(ids, ",") != want {
				t.Errorf("freebusy IDs = %v, want %s", ids, want)
			}
			result := map[string]gcal.FreeBusyCalendar{}
			for _, id := range ids {
				result[id] = gcal.FreeBusyCalendar{}
			}
			if !explicit {
				result["school"] = gcal.FreeBusyCalendar{Busy: []*gcal.TimePeriod{{Start: "2026-10-06T09:00:00-07:00", End: "2026-10-06T10:00:00-07:00"}}}
				if sharedFailure {
					result["school"] = gcal.FreeBusyCalendar{Errors: []*gcal.Error{{Reason: "notFound"}}}
				}
			}
			write(&gcal.FreeBusyResponse{Calendars: result})
		default:
			t.Errorf("unexpected request: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	api, err := gcal.NewService(context.Background(), option.WithEndpoint(ts.URL+"/"), option.WithoutAuthentication(), option.WithHTTPClient(ts.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return &Service{svc: api}
}

func TestReadDefaultsIncludeSharedCalendarOnLaterPage(t *testing.T) {
	for _, mode := range []string{"list", "search", "freebusy"} {
		for _, explicit := range []bool{false, true} {
			name := mode + "/all"
			if explicit {
				name = mode + "/primary"
			}
			t.Run(name, func(t *testing.T) {
				s := calendarFixture(t, explicit, false)
				id := ""
				if explicit {
					id = "primary"
				}
				start := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
				var events []*EventSummary
				var err error
				switch mode {
				case "list":
					events, err = s.ListEvents(context.Background(), id, start, start.Add(24*time.Hour), 50)
				case "search":
					events, err = s.SearchEvents(context.Background(), id, "Biology", 50)
				case "freebusy":
					var ids []string
					if explicit {
						ids = []string{id}
					}
					busy, err := s.GetFreeBusy(context.Background(), ids, start, start.Add(24*time.Hour))
					if err != nil {
						t.Fatal(err)
					}
					if !explicit && len(busy["school"].Busy) != 1 {
						t.Fatalf("shared busy time lost: %+v", busy)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if explicit {
					if len(events) != 0 {
						t.Fatalf("primary-only lookup leaked events: %+v", events)
					}
				} else if len(events) != 1 || events[0].ID != "lab" || events[0].CalendarID != "school" || events[0].CalendarName != "Owner School" {
					t.Fatalf("shared calendar event missing or unlabelled: %+v", events)
				}
			})
		}
	}
}

func TestSharedCalendarErrorsDoNotLookEmptyOrFree(t *testing.T) {
	for _, mode := range []string{"list", "search", "freebusy"} {
		t.Run(mode, func(t *testing.T) {
			s := calendarFixture(t, false, true)
			var err error
			switch mode {
			case "list":
				_, err = s.ListEvents(context.Background(), "", time.Now(), time.Now().Add(time.Hour), 50)
			case "search":
				_, err = s.SearchEvents(context.Background(), "", "Biology", 50)
			case "freebusy":
				_, err = s.GetFreeBusy(context.Background(), nil, time.Now(), time.Now().Add(time.Hour))
			}
			if err == nil || !strings.Contains(err.Error(), "school") {
				t.Fatalf("want identifiable shared-calendar failure, got %v", err)
			}
		})
	}
}

func TestFreeBusyBatchesAndReportsAllErrors(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail=%t", fail), func(t *testing.T) {
			var requests atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var req gcal.FreeBusyRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if len(req.Items) > 50 || len(req.Items) == 0 {
					t.Errorf("invalid batch size %d", len(req.Items))
				}
				calendars := map[string]gcal.FreeBusyCalendar{}
				for _, item := range req.Items {
					calendars[item.Id] = gcal.FreeBusyCalendar{}
				}
				if fail {
					calendars["calendar-00"] = gcal.FreeBusyCalendar{Errors: []*gcal.Error{{}}}
					calendars["calendar-01"] = gcal.FreeBusyCalendar{Errors: []*gcal.Error{{Reason: "notFound"}, {Reason: "internalError"}}}
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(&gcal.FreeBusyResponse{Calendars: calendars}); err != nil {
					t.Error(err)
				}
			}))
			defer ts.Close()
			api, err := gcal.NewService(context.Background(), option.WithEndpoint(ts.URL+"/"), option.WithoutAuthentication(), option.WithHTTPClient(ts.Client()))
			if err != nil {
				t.Fatal(err)
			}
			s := &Service{svc: api}
			ids := make([]string, 51)
			for i := range ids {
				ids[i] = fmt.Sprintf("calendar-%02d", i)
			}
			got, err := s.GetFreeBusy(context.Background(), ids, time.Now(), time.Now().Add(time.Hour))
			if fail {
				want := `freebusy: calendar "calendar-00": unknown error; calendar "calendar-01": notFound, internalError`
				if err == nil || err.Error() != want || got != nil {
					t.Fatalf("got %+v, %v; want %s", got, err, want)
				}
			} else if err != nil || len(got) != 51 || requests.Load() != 2 {
				t.Fatalf("51-calendar query = %d calendars, %d requests, %v", len(got), requests.Load(), err)
			}
		})
	}
}
