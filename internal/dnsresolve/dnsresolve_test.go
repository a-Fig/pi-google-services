package dnsresolve

import (
	"context"
	"net"
	"reflect"
	"testing"
)

func TestServerListParsesAndAppendsFallback(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "empty falls back to public only",
			raw:  "",
			want: publicFallback,
		},
		{
			name: "ipv4 servers get :53 and fallback appended",
			raw:  "192.168.1.1,10.0.0.2",
			want: []string{"192.168.1.1:53", "10.0.0.2:53", "1.1.1.1:53", "8.8.8.8:53"},
		},
		{
			name: "ipv6 servers get bracketed",
			raw:  "2001:4860:4860::8888",
			want: []string{"[2001:4860:4860::8888]:53", "1.1.1.1:53", "8.8.8.8:53"},
		},
		{
			name: "whitespace around entries is trimmed",
			raw:  " 8.8.4.4 , 9.9.9.9 ",
			want: []string{"8.8.4.4:53", "9.9.9.9:53", "1.1.1.1:53", "8.8.8.8:53"},
		},
		{
			name: "malformed entries are dropped, not fatal",
			raw:  "not-an-ip,192.168.1.1,,",
			want: []string{"192.168.1.1:53", "1.1.1.1:53", "8.8.8.8:53"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := serverList(tc.raw)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("serverList(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// fakeListener accepts one connection so dialer's DialContext has something reachable to
// prove it picks a working server rather than always hitting the same (possibly dead) first
// entry.
func fakeListener(t *testing.T) (addr string, close func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return l.Addr().String(), func() { l.Close() }
}

func TestDialerFallsThroughDeadServers(t *testing.T) {
	good, closeGood := fakeListener(t)
	defer closeGood()

	// 127.0.0.1:1 is refused immediately (nothing listens on port 1); the dialer must not
	// get stuck there and must still reach the good listener.
	d := dialer([]string{"127.0.0.1:1", good})
	conn, err := d(context.Background(), "tcp", "ignored")
	if err != nil {
		t.Fatalf("dialer returned error, want it to fall through to the working server: %v", err)
	}
	conn.Close()
}

func TestDialerNoServersConfigured(t *testing.T) {
	d := dialer(nil)
	if _, err := d(context.Background(), "udp", "ignored"); err == nil {
		t.Fatal("want an error when no servers are configured")
	}
}
