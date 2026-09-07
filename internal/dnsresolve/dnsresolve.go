// Package dnsresolve installs a net.Resolver that dials the phone's real DNS servers instead
// of Go's built-in pure-Go resolver.
//
// Background (incident 2026-09-07, Gmail sign-in on the Pixel): both connector binaries
// (pi-google-services and email-watch) are cross-compiled with CGO_ENABLED=0, so Go never
// links against Bionic's getaddrinfo and always uses its own pure-Go DNS client. That client's
// only source of nameservers is /etc/resolv.conf, which does not exist on Android; with no
// servers configured it falls back to 127.0.0.1:53 / [::1]:53, where nothing listens. The
// result is exactly the failure the user hit:
//
//	dial tcp: lookup oauth2.googleapis.com on [::1]:53: read udp [::1]:49185->[::1]:53:
//	read: connection refused
//
// This can never resolve on any Android phone, with or without a VPN, regardless of network
// state -- it is not a transient failure.
//
// Rejected alternative: rebuilding with CGO_ENABLED=1 (Termux clang) so Go uses Bionic's
// resolver directly. That handles VPN/Private DNS/split-horizon automatically, but a
// Termux-built cgo binary risks being linked against paths under /data/data/com.termux/...
// that this app's uid cannot read -- the same class of bug already hit once with Node's
// compiled-in OPENSSL_CONF pointing at a Termux path (android/node/README.md). Proving a cgo
// build clean of that would need the NDK, which is not installed here. Kept pure Go instead.
//
// The fix: the Android app reads the phone's actual DNS servers via
// ConnectivityManager.getLinkProperties(activeNetwork).dnsServers (see
// android/app/vi/src/main/java/com/heyvi/vi/net/DnsServers.kt) and passes them to this
// process as PI_GOOGLE_DNS_SERVERS, a comma-separated list of bare IP addresses (no port, no
// brackets -- Install adds ":53" itself so IPv6 addresses don't need pre-bracketing by the
// caller). Install() then points net.DefaultResolver's dial at that list, tried in rotation.
//
// A public resolver (Cloudflare and Google, in that order) is appended to the list, not used
// as the exclusive fallback for an empty env var: if the phone's real servers were valid when
// the Node daemon started but the network has since changed (Wi-Fi to cellular, VPN toggled)
// there is no live channel back to this already-running process to refresh them -- see
// ServeEnv's doc comment. Falling through to a public resolver after the phone's own servers
// fail is what keeps that scenario from reproducing this exact incident; it is not the primary
// mechanism; and it is only ever tried after every phone-supplied server has failed.
package dnsresolve

import (
	"context"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// ServersEnv is the environment variable the Android app sets before spawning the Node
// daemon (ViForegroundService.kt), which Node's child_process.spawn then inherits down into
// this binary (see app/daemon/src/channels/gmail/index.ts's `env(context)` -- a plain spread
// of process.env, no per-connector plumbing needed). It is read once, at process start:
// there is no mechanism here to notice a later network change without a full daemon restart.
const ServersEnv = "PI_GOOGLE_DNS_SERVERS"

// publicFallback is tried only after every server from ServersEnv has failed (or when the env
// var is empty/unset). Cloudflare first: no logging by policy, matters less here than for a
// browser but keeps this consistent with other Vi components. Never the primary mechanism --
// see the package doc for why a public server alone is not enough (a saved answer can be
// wrong for a captive portal or split-horizon network) and why it is still needed as a last
// resort (a stale or momentarily-unreachable phone-supplied server should not wedge every
// lookup this process ever makes again).
var publicFallback = []string{"1.1.1.1:53", "8.8.8.8:53"}

// dialTimeout bounds each individual server dial; Install rotates to the next server rather
// than waiting out a dead one, so this can stay well under an overall request deadline.
const dialTimeout = 3 * time.Second

// Install points net.DefaultResolver at the servers named by PI_GOOGLE_DNS_SERVERS, falling
// back to a public resolver when that list is empty or exhausted. Call it once, at the very
// top of main(), before anything else touches the network -- both pi-google-services and
// email-watch do.
func Install() {
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial:     dialer(serverList(os.Getenv(ServersEnv))),
	}
}

// serverList parses ServersEnv's comma-separated bare IP addresses into "ip:53" pairs (via
// net.JoinHostPort, so IPv6 addresses are bracketed correctly) and appends publicFallback.
// Malformed entries are dropped rather than failing outright -- one bad address from a link
// properties query that returned something odd should not cost the good ones.
func serverList(raw string) []string {
	var servers []string
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if net.ParseIP(s) == nil {
			continue
		}
		servers = append(servers, net.JoinHostPort(s, "53"))
	}
	return append(servers, publicFallback...)
}

// dialer returns a net.Resolver.Dial func that tries each server in servers, in rotation (so
// repeated calls don't all serialize behind the same dead first server), returning the first
// successful connection. network and address are what Go's resolver itself passes in ("udp"
// or "tcp" on retry, and the address it would have used from /etc/resolv.conf); address is
// ignored on purpose -- these are the phone's real servers, not what a missing resolv.conf
// would have said.
func dialer(servers []string) func(ctx context.Context, network, address string) (net.Conn, error) {
	var next int32
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		n := len(servers)
		if n == 0 {
			// serverList always appends publicFallback, so this only happens if a caller
			// builds a dialer directly with an empty slice (tests).
			return nil, &net.DNSError{Err: "no DNS servers configured", IsTemporary: true}
		}
		start := int(atomic.AddInt32(&next, 1)) % n
		d := net.Dialer{Timeout: dialTimeout}
		var lastErr error
		for i := 0; i < n; i++ {
			conn, err := d.DialContext(ctx, network, servers[(start+i)%n])
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}
