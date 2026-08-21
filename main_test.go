package main

import "testing"

func TestWantsNoBrowser(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "no args", args: nil, want: false},
		{name: "plain login", args: []string{"pi-google-services", "login"}, want: false},
		{name: "login with flag after subcommand", args: []string{"pi-google-services", "login", "--no-browser"}, want: true},
		{name: "setup with flag after subcommand", args: []string{"pi-google-services", "setup", "--no-browser"}, want: true},
		{name: "other flags do not trigger it", args: []string{"pi-google-services", "login", "--verbose"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wantsNoBrowser(tt.args); got != tt.want {
				t.Errorf("wantsNoBrowser(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
