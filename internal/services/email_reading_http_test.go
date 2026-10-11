package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sombi/pi-google-services/internal/gmail"
	"golang.org/x/oauth2"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

type mailFixtureTransport func(*http.Request) (*http.Response, error)

func (f mailFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixtureGmail(t *testing.T) *GmailService {
	t.Helper()
	client := &http.Client{Transport: mailFixtureTransport(func(r *http.Request) (*http.Response, error) {
		payload := func(id string) *gmailapi.Message {
			return &gmailapi.Message{Id: id, ThreadId: "t", Payload: &gmailapi.MessagePart{MimeType: "multipart/mixed", Headers: []*gmailapi.MessagePartHeader{{Name: "From", Value: "sender@example.test"}, {Name: "Subject", Value: "Fixture"}, {Name: "Authentication-Results", Value: "mx; dmarc=fail"}}, Parts: []*gmailapi.MessagePart{{MimeType: "text/plain", Body: &gmailapi.MessagePartBody{Data: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 5001)))}}, {Filename: "note.txt", MimeType: "text/plain", Body: &gmailapi.MessagePartBody{AttachmentId: "a", Size: 7}}}}}
		}
		var value interface{}
		if strings.Contains(r.URL.Path, "/threads/") {
			messages := []*gmailapi.Message{}
			for _, id := range []string{"m0", "m1", "m2", "m3", "m4", "m5"} {
				messages = append(messages, payload(id))
			}
			value = &gmailapi.Thread{Id: "t", Messages: messages}
		} else if strings.Contains(r.URL.Path, "/messages/") {
			value = payload("m0")
		} else {
			t.Errorf("unexpected synthetic path: %s", r.URL.Path)
		}
		data, err := json.Marshal(value)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(data))), Request: r}, err
	})}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, client)
	api, err := gmail.New(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "synthetic-fixture-only"}), option.WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	return NewGmail(api, nil)
}
func resultJSON(t *testing.T, value interface{}) map[string]interface{} {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err = json.Unmarshal(encoded, &envelope); err != nil || len(envelope.Content) != 1 {
		t.Fatalf("envelope: %s %v", encoded, err)
	}
	var result map[string]interface{}
	if err = json.Unmarshal([]byte(envelope.Content[0].Text), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestEmailHandlerContinuationAndThreadPages(t *testing.T) {
	service := fixtureGmail(t)
	value, err := service.Handle(context.Background(), "get-email", json.RawMessage(`{"id":"m0","format":"json"}`))
	if err != nil {
		t.Fatal(err)
	}
	first := resultJSON(t, value)
	if first["dmarc"] != "fail" {
		t.Fatalf("lost DMARC: %+v", first)
	}
	if first["nextOffset"] != float64(5000) || len(first["body"].(string)) != 5000 || len(first["attachments"].([]interface{})) != 1 {
		t.Fatalf("first: %+v", first)
	}
	value, err = service.Handle(context.Background(), "get-email", json.RawMessage(`{"id":"m0","offset":5000,"format":"json"}`))
	if err != nil {
		t.Fatal(err)
	}
	last := resultJSON(t, value)
	if last["body"] != "x" || last["nextOffset"] != nil {
		t.Fatalf("last: %+v", last)
	}
	value, err = service.Handle(context.Background(), "get-thread", json.RawMessage(`{"threadId":"t"}`))
	if err != nil {
		t.Fatal(err)
	}
	thread := resultJSON(t, value)
	if thread["nextOffset"] != float64(5) || len(thread["messages"].([]interface{})) != 5 {
		t.Fatalf("thread: %+v", thread)
	}
	value, err = service.Handle(context.Background(), "get-thread", json.RawMessage(`{"threadId":"t","offset":5}`))
	if err != nil {
		t.Fatal(err)
	}
	lastThread := resultJSON(t, value)
	if lastThread["nextOffset"] != nil || len(lastThread["messages"].([]interface{})) != 1 {
		t.Fatalf("last thread: %+v", lastThread)
	}
	_, err = service.Handle(context.Background(), "get-email", json.RawMessage(`{"id":"m0","offset":5002}`))
	if err == nil || err.Code != -32602 {
		t.Fatal("invalid body offset accepted")
	}
	_, err = service.Handle(context.Background(), "get-thread", json.RawMessage(`{"threadId":"t","offset":7}`))
	if err == nil || err.Code != -32602 {
		t.Fatal("invalid message offset accepted")
	}
}
