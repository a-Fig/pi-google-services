package main

import (
	"testing"

	"github.com/sombi/pi-google-services/internal/gmail"
)

var tylerAddresses = []string{"tylerd2474@gmail.com", "tyler.darisme@sjsu.edu"}

func TestAuthoredMessage(t *testing.T) {
	tests := []struct {
		name    string
		message *gmail.EmailSummary
		want    bool
	}{
		{"direct", &gmail.EmailSummary{From: "Tyler <tylerd2474@gmail.com>", ReturnPath: "<tylerd2474@gmail.com>"}, true},
		{"automatic forward", &gmail.EmailSummary{From: "Canvas <notifications@instructure.com>", ReturnPath: "<mail@instructure.com>", XForwardedTo: "vi.secretary.tyler@gmail.com"}, false},
		{"generated on Tyler's behalf", &gmail.EmailSummary{From: "Tyler <tylerd2474@gmail.com>", ReturnPath: "<calendar-notification@google.com>"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := authoredMessage(tt.message, tylerAddresses); got != tt.want {
				t.Fatalf("authoredMessage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClassifyOrigin(t *testing.T) {
	forwarded := &gmail.EmailSummary{Subject: "Fwd: useful email"}
	classifyOrigin(forwarded)
	if forwarded.Origin != "manual-forward" {
		t.Fatalf("forwarded origin = %q", forwarded.Origin)
	}
	direct := &gmail.EmailSummary{Subject: "note for Vi"}
	classifyOrigin(direct)
	if direct.Origin != "direct" {
		t.Fatalf("direct origin = %q", direct.Origin)
	}
}
