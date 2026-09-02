package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sombi/pi-google-services/internal/calendar"
	"github.com/sombi/pi-google-services/internal/mcp"
)

// mockCalendarAPI implements calendar operations without real API calls.
type mockCalendarAPI struct {
	events []*calendar.EventSummary
}

func (m *mockCalendarAPI) ListEvents(ctx context.Context, calendarID string, timeMin, timeMax time.Time, maxResults int64) ([]*calendar.EventSummary, error) {
	return m.events, nil
}
func (m *mockCalendarAPI) CreateEvent(ctx context.Context, calendarID string, event interface{}) (interface{}, error) {
	return map[string]interface{}{
		"id":       "mock-event-1",
		"summary":  "test",
		"htmlLink": "https://calendar.google.com/event?eid=mock",
	}, nil
}
func (m *mockCalendarAPI) DeleteEvent(ctx context.Context, calendarID, eventID string) error {
	return nil
}
func (m *mockCalendarAPI) SearchEvents(ctx context.Context, query string, maxResults int64) ([]*calendar.EventSummary, error) {
	return m.events, nil
}
func (m *mockCalendarAPI) ListCalendars(ctx context.Context) ([]interface{}, error) {
	return []interface{}{}, nil
}
func (m *mockCalendarAPI) GetFreeBusy(ctx context.Context, calendarIDs []string, timeMin, timeMax time.Time) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}

// We test the service layer by using its tool definitions and handler routing.
func TestCalendarServiceName(t *testing.T) {
	cs := &CalendarService{}
	if cs.Name() != "calendar" {
		t.Errorf("Name() = %q, want %q", cs.Name(), "calendar")
	}
}

func TestCalendarServiceScopes(t *testing.T) {
	cs := &CalendarService{}
	scopes := cs.Scopes()
	if len(scopes) < 2 {
		t.Errorf("expected at least 2 scopes, got %d", len(scopes))
	}
}

func TestCalendarServiceTools(t *testing.T) {
	cs := &CalendarService{}
	tools := cs.Tools()
	if len(tools) != 7 {
		t.Errorf("expected 7 tools, got %d", len(tools))
	}

	// Verify all expected tool names are present
	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, name := range []string{
		"list-events", "create-event", "update-event",
		"delete-event", "search-events", "list-calendars", "get-freebusy",
	} {
		if !names[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestCalendarToolSchemasExposeSharedCalendarFeatures(t *testing.T) {
	tools := (&CalendarService{}).Tools()
	byName := make(map[string]map[string]mcp.PropertySchema, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool.InputSchema.Properties
	}

	for _, toolName := range []string{"create-event", "update-event"} {
		if _, ok := byName[toolName]["calendarId"]; !ok {
			t.Errorf("%s missing calendarId", toolName)
		}
		if _, ok := byName[toolName]["colorId"]; !ok {
			t.Errorf("%s missing colorId", toolName)
		}
	}
	if _, ok := byName["search-events"]["calendarId"]; !ok {
		t.Error("search-events missing calendarId")
	}
}

func TestFormatEventsIncludesIDsCalendarAndColor(t *testing.T) {
	got := formatEvents([]*calendar.EventSummary{{
		ID:         "event-123",
		CalendarID: "school@example.com",
		ColorID:    "11",
		Summary:    "Calc 1 Exam",
		Start:      "Sun Aug 30 13:00 PDT",
		End:        "Sun Aug 30 15:00 PDT",
	}})
	for _, want := range []string{"event-123", "school@example.com", "Color ID: 11"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatEvents() missing %q in %q", want, got)
		}
	}
}

func TestCalendarRPCErrorSurfacesGoogleDetail(t *testing.T) {
	rpcErr := calendarRPCError("create event", errors.New("requiredAccessLevel"))
	if !strings.Contains(rpcErr.Message, "requiredAccessLevel") {
		t.Fatalf("error message hid API detail: %q", rpcErr.Message)
	}
}

func TestCalendarServiceHandle_UnknownTool(t *testing.T) {
	cs := &CalendarService{}
	_, err := cs.Handle(nil, "nonexistent", nil)
	if err == nil || err.Code != -32601 {
		t.Errorf("expected -32601 error, got %+v", err)
	}
}

func TestGmailServiceName(t *testing.T) {
	gs := &GmailService{}
	if gs.Name() != "gmail" {
		t.Errorf("Name() = %q, want %q", gs.Name(), "gmail")
	}
}

func TestGmailServiceScopes(t *testing.T) {
	gs := &GmailService{}
	scopes := gs.Scopes()
	if len(scopes) < 2 {
		t.Errorf("expected at least 2 scopes, got %d", len(scopes))
	}
}

func TestGmailServiceTools(t *testing.T) {
	gs := &GmailService{}
	tools := gs.Tools()
	if len(tools) != 6 {
		t.Errorf("expected 6 tools, got %d", len(tools))
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, name := range []string{
		"list-inbox", "get-email", "search-emails",
		"send-email", "reply-to-email", "download-attachment",
	} {
		if !names[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestGmailServiceHandle_UnknownTool(t *testing.T) {
	gs := &GmailService{}
	_, err := gs.Handle(nil, "nonexistent", nil)
	if err == nil || err.Code != -32601 {
		t.Errorf("expected -32601 error, got %+v", err)
	}
}

// TestListEventsHandlers validates parameter parsing.
func TestListEventsArgs(t *testing.T) {
	// Test that the time parsing doesn't panic on empty
	var args struct {
		CalendarID string `json:"calendarId"`
		TimeMin    string `json:"timeMin"`
		TimeMax    string `json:"timeMax"`
		MaxResults int64  `json:"maxResults"`
	}

	// Empty JSON should produce zero values
	data := []byte(`{}`)
	if err := json.Unmarshal(data, &args); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Valid time should parse
	args.TimeMin = "2026-06-16T10:00:00-03:00"
	tm, err := time.Parse(time.RFC3339, args.TimeMin)
	if err != nil {
		t.Fatalf("time parse: %v", err)
	}
	if tm.Year() != 2026 || tm.Month() != 6 || tm.Day() != 16 {
		t.Errorf("unexpected date: %v", tm)
	}
}

// --- Drive service tests ---

func TestDriveServiceName(t *testing.T) {
	ds := &DriveService{}
	if ds.Name() != "drive" {
		t.Errorf("Name() = %q, want %q", ds.Name(), "drive")
	}
}

func TestDriveServiceScopes(t *testing.T) {
	ds := &DriveService{}
	scopes := ds.Scopes()
	if len(scopes) < 1 {
		t.Errorf("expected at least 1 scope, got %d", len(scopes))
	}
}

func TestDriveServiceTools(t *testing.T) {
	ds := &DriveService{}
	tools := ds.Tools()
	if len(tools) != 6 {
		t.Errorf("expected 6 tools, got %d", len(tools))
	}
	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, name := range []string{
		"list-files", "search-drive", "upload-file",
		"download-file", "create-folder", "delete-file",
	} {
		if !names[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestDriveServiceHandle_UnknownTool(t *testing.T) {
	ds := &DriveService{}
	_, err := ds.Handle(nil, "nonexistent", nil)
	if err == nil || err.Code != -32601 {
		t.Errorf("expected -32601 error, got %+v", err)
	}
}

func TestServiceToolsCount(t *testing.T) {
	calLen := len((&CalendarService{}).Tools())
	gmailLen := len((&GmailService{}).Tools())
	tasksLen := len((&TasksService{}).Tools())
	driveLen := len((&DriveService{}).Tools())
	contactsLen := len((&ContactsService{}).Tools())

	if calLen != 7 {
		t.Errorf("Calendar: expected 7, got %d", calLen)
	}
	if gmailLen != 6 {
		t.Errorf("Gmail: expected 6, got %d", gmailLen)
	}
	if tasksLen != 5 {
		t.Errorf("Tasks: expected 5, got %d", tasksLen)
	}
	if driveLen != 6 {
		t.Errorf("Drive: expected 6, got %d", driveLen)
	}
	if contactsLen != 3 {
		t.Errorf("Contacts: expected 3, got %d", contactsLen)
	}
	total := calLen + gmailLen + tasksLen + driveLen + contactsLen
	if total != 27 {
		t.Errorf("Total tools: expected 27 (7+6+5+6+3), got %d", total)
	}
}

func TestContactsServiceName(t *testing.T) {
	ds := &ContactsService{}
	if ds.Name() != "contacts" {
		t.Errorf("Name() = %q, want %q", ds.Name(), "contacts")
	}
}

func TestContactsServiceScopes(t *testing.T) {
	ds := &ContactsService{}
	scopes := ds.Scopes()
	if len(scopes) < 1 {
		t.Errorf("expected at least 1 scope, got %d", len(scopes))
	}
}

func TestContactsServiceTools(t *testing.T) {
	ds := &ContactsService{}
	tools := ds.Tools()
	if len(tools) != 3 {
		t.Errorf("expected 3 tools, got %d", len(tools))
	}
	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, name := range []string{"search-contacts", "get-contact", "create-contact"} {
		if !names[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}
