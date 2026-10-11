package auth

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/sombi/pi-google-services/internal/config"
)

// TestMain points the config directory at a throwaway location for every test in
// this package.
//
// This is a safety net, not a nicety. The login tests complete a full OAuth flow
// against a fixture token and persist the result. If any of them reaches the real
// config directory, it overwrites the user's live Google credentials with
// "fake-at"/"fake-rt", and the only symptom is invalid_grant on every subsequent
// API call -- with a token file that still looks structurally valid, and a
// `status` command that still reports "authenticated".
//
// That is not hypothetical: four of these tests used `defer withTempConfigDir(t)`,
// which defers the redirect until after the test body has already written to the
// real path. Running `go test ./...` silently destroyed a live deployment's
// credentials, twice. Per-test redirection is still correct and still used; this
// makes a mistake in it harmless instead of destructive.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pi-google-services-auth-test")
	if err != nil {
		panic(err)
	}
	config.Dir = func() (string, error) { return dir, nil }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestParseAuthCode(t *testing.T) {
	tests := []struct {
		name    string
		pasted  string
		want    string
		wantErr bool
	}{
		{
			name:   "full callback URL with code",
			pasted: "http://localhost:49152/oauth/callback?state=state&code=4/0AXXXyyy&scope=email",
			want:   "4/0AXXXyyy",
		},
		{
			name:   "https callback URL",
			pasted: "http://localhost:8080/oauth/callback?code=abc.def-ghi",
			want:   "abc.def-ghi",
		},
		{
			name:   "raw code only",
			pasted: "4/0AaSdBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789",
			want:   "4/0AaSdBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789",
		},
		{
			name:   "URL-encoded code gets decoded",
			pasted: "http://localhost:8080/oauth/callback?code=4%2F0AQSTVwxYz&scope=calendar",
			want:   "4/0AQSTVwxYz",
		},
		{
			name:   "surrounding whitespace and newline trimmed",
			pasted: "  http://localhost:8080/oauth/callback?code=xyz123\n",
			want:   "xyz123",
		},
		{
			name:    "error param access_denied reports denial",
			pasted:  "http://localhost:8080/oauth/callback?error=access_denied&error_description=User+denied+access",
			wantErr: true,
		},
		{
			name:    "empty input",
			pasted:  "",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			pasted:  "   \n\t ",
			wantErr: true,
		},
		{
			name:    "URL without code",
			pasted:  "http://localhost:8080/oauth/callback?state=state",
			wantErr: true,
		},
		{
			name:    "multi-word garbage is not accepted as raw code",
			pasted:  "oops let me find it",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAuthCode(tt.pasted)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseAuthCode(%q) = %q, want error", tt.pasted, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAuthCode(%q): %v", tt.pasted, err)
			}
			if got != tt.want {
				t.Errorf("ParseAuthCode(%q) = %q, want %q", tt.pasted, got, tt.want)
			}
		})
	}
}

func TestPromptForAuthCode(t *testing.T) {
	const testURL = "https://accounts.google.com/o/oauth2/auth?client_id=x"

	tests := []struct {
		name      string
		input     string
		wantCode  string
		wantErr   bool
		errDenied bool
		wantURLIn int // how many times authURL must appear in output
	}{
		{
			name:      "valid URL pasted first try",
			input:     "http://localhost:8080/oauth/callback?code=goodcode\n",
			wantCode:  "goodcode",
			wantURLIn: 1,
		},
		{
			name:      "garbage then valid code",
			input:     "oops let me find it\n\nhttp://localhost:8080/oauth/callback?code=second\n",
			wantCode:  "second",
			wantURLIn: 2,
		},
		{
			name:      "access_denied fails immediately",
			input:     "http://localhost:8080/oauth/callback?error=access_denied\nmore input that must not be read\n",
			wantErr:   true,
			errDenied: true,
			wantURLIn: 1,
		},
		{
			name:      "EOF without valid code",
			input:     "",
			wantErr:   true,
			wantURLIn: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			in := strings.NewReader(tt.input)

			code, err := promptForAuthCode(testURL, in, &out)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("promptForAuthCode() = %q, want error", code)
				}
				if tt.errDenied && !strings.Contains(err.Error(), "denied") {
					t.Errorf("error = %q, want it to mention denial", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("promptForAuthCode(): %v", err)
			}
			if code != tt.wantCode {
				t.Errorf("promptForAuthCode() = %q, want %q", code, tt.wantCode)
			}
			if got := strings.Count(out.String(), testURL); got < tt.wantURLIn {
				t.Errorf("authURL shown %d times, want at least %d\noutput:\n%s", got, tt.wantURLIn, out.String())
			}
		})
	}
}

// newTestAuthenticator builds an Authenticator whose token endpoint points
// at a fake Google server returning a fixed token payload.
func newTestAuthenticator(t *testing.T, tokenJSON string) (*Authenticator, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("fake token endpoint: bad form: %v", err)
		}
		if r.Form.Get("code_verifier") == "" && r.Form.Get("grant_type") != "refresh_token" {
			t.Errorf("token request missing code_verifier (PKCE): %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, tokenJSON)
	}))
	t.Cleanup(ts.Close)

	a := &Authenticator{
		OAuthConfig: &oauth2.Config{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			Scopes:       []string{"email"},
			Endpoint: oauth2.Endpoint{
				AuthURL:  ts.URL + "/auth",
				TokenURL: ts.URL + "/token",
			},
			RedirectURL: "http://localhost:1/oauth/callback",
		},
	}
	return a, ts
}

func withTempConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := config.Dir
	config.Dir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { config.Dir = old })
	return dir
}

const fakeTokenJSON = `{"access_token":"fake-at","refresh_token":"fake-rt","token_type":"Bearer","expires_in":3600}`

func TestLoginManualMode(t *testing.T) {
	withTempConfigDir(t)
	a, _ := newTestAuthenticator(t, fakeTokenJSON)

	var out strings.Builder
	in := strings.NewReader("pasted-code\n")

	token, err := a.LoginWithOptions(context.Background(), LoginOptions{
		NoBrowser: true,
		Input:     io.NopCloser(in),
		Output:    &out,
	})
	if err != nil {
		t.Fatalf("LoginWithOptions(manual): %v", err)
	}
	if token.AccessToken != "fake-at" || !token.Valid() {
		t.Errorf("token = %+v, want access_token=fake-at and valid", token)
	}
	if !strings.Contains(out.String(), "/auth?") {
		t.Error("manual output must show the auth URL")
	}
}

func TestLoginBrowserFailureFallsBackToManual(t *testing.T) {
	withTempConfigDir(t)
	a, _ := newTestAuthenticator(t, fakeTokenJSON)

	var out strings.Builder
	in := strings.NewReader("raw-pasted-code-123\n")

	token, err := a.LoginWithOptions(context.Background(), LoginOptions{
		Input:       io.NopCloser(in),
		Output:      &out,
		OpenBrowser: func(url string) error { return fmt.Errorf("no browser here") },
	})
	if err != nil {
		t.Fatalf("LoginWithOptions(fallback): %v", err)
	}
	if token.AccessToken != "fake-at" {
		t.Errorf("access_token = %q, want fake-at", token.AccessToken)
	}
	if s := out.String(); !strings.Contains(s, "no-browser") && !strings.Contains(s, "No browser") {
		t.Errorf("output should explain manual mode, got:\n%s", s)
	}
}

func TestLoginBrowserFlowEndToEnd(t *testing.T) {
	withTempConfigDir(t)
	a, _ := newTestAuthenticator(t, fakeTokenJSON)

	// Simulate Google: capture the auth URL from the browser stub, resolve
	// its loopback redirect target, and hit the callback with a code.
	openStub := func(authURL string) error {
		go func() {
			u, err := url.Parse(authURL)
			if err != nil {
				return
			}
			redirect := u.Query().Get("redirect_uri")
			ru, err := url.Parse(redirect)
			if err != nil {
				return
			}
			callback := fmt.Sprintf("http://localhost:%s%s?state=%s&code=e2e-code", ru.Port(), ru.Path, u.Query().Get("state"))
			http.Get(callback) //nolint:errcheck // best-effort simulation
		}()
		return nil
	}

	var out strings.Builder

	token, err := a.LoginWithOptions(context.Background(), LoginOptions{
		Input:       io.NopCloser(strings.NewReader("")),
		Output:      &out,
		OpenBrowser: openStub,
	})
	if err != nil {
		t.Fatalf("LoginWithOptions(browser): %v", err)
	}
	if token.AccessToken != "fake-at" {
		t.Errorf("access_token = %q, want fake-at", token.AccessToken)
	}
}

type staticTokenSource struct {
	token *oauth2.Token
	err   error
}

func (s staticTokenSource) Token() (*oauth2.Token, error) { return s.token, s.err }

func TestPersistingTokenSourceSavesOnlyChangedTokens(t *testing.T) {
	token := &oauth2.Token{AccessToken: "fresh", RefreshToken: "refresh"}
	saves := 0
	source := &persistingTokenSource{
		source: staticTokenSource{token: token},
		save: func(got *oauth2.Token) error {
			saves++
			if got.AccessToken != "fresh" {
				t.Errorf("saved access token = %q", got.AccessToken)
			}
			return nil
		},
	}

	if _, err := source.Token(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Token(); err != nil {
		t.Fatal(err)
	}
	if saves != 1 {
		t.Fatalf("saved %d times, want 1", saves)
	}
}

func TestLoginManualModeUsesRealLoopbackPort(t *testing.T) {
	withTempConfigDir(t)
	a, _ := newTestAuthenticator(t, fakeTokenJSON)

	var out strings.Builder
	in := strings.NewReader("c\n")

	if _, err := a.LoginWithOptions(context.Background(), LoginOptions{
		NoBrowser: true,
		Input:     io.NopCloser(in),
		Output:    &out,
	}); err != nil {
		t.Fatalf("LoginWithOptions(manual): %v", err)
	}

	// Regression: the redirect URL must carry a real allocated port,
	// not the placeholder port 0 (Google rejects invalid loopback ports).
	re := regexp.MustCompile(`http://localhost:(\d+)/oauth/callback`)
	m := re.FindStringSubmatch(a.OAuthConfig.RedirectURL)
	if m == nil {
		t.Fatalf("RedirectURL = %q, want localhost:<port>/oauth/callback", a.OAuthConfig.RedirectURL)
	}
	port, err := strconv.Atoi(m[1])
	if err != nil || port <= 0 || port > 65535 {
		t.Errorf("RedirectURL port = %q, want a valid TCP port", m[1])
	}
}

// A manual login on the same host can receive the callback without any paste.
func TestManualCallbackCompletesAndClosesReader(t *testing.T) {
	withTempConfigDir(t)
	input, writer := io.Pipe()
	defer writer.Close()
	var output bytes.Buffer
	cb, cleanup, err := startCallbackServer("expected-state")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	done := make(chan error, 1)
	go func() {
		code, err := waitForAuthCode(context.Background(), "https://example.test/auth?state=expected-state", input, &output, cb)
		if code != "received" && err == nil {
			err = fmt.Errorf("wrong code: %s", code)
		}
		done <- err
	}()
	response, err := http.Get(cb.url + "?state=wrong&code=wrong")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatal("wrong state accepted")
	}
	response, err = http.Get(cb.url + "?state=expected-state&code=received")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if strings.Contains(string(body), "Authorized!") || !strings.Contains(string(body), "still finishing") {
		t.Fatalf("misleading callback: %s", body)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("manual reader did not drain")
	}
	// A duplicate never blocks, even when the first result was already consumed.
	response, err = http.Get(cb.url + "?state=expected-state&code=duplicate")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if _, err := writer.Write([]byte("late-code\n")); err == nil {
		t.Fatal("reader remained open")
	}
}

func TestManualWaitCancellationDrainsInput(t *testing.T) {
	input, writer := io.Pipe()
	defer writer.Close()
	cb, cleanup, err := startCallbackServer("state")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := waitForAuthCode(ctx, "https://example.test/auth?state=state", input, io.Discard, cb)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation leaked reader")
	}
}

func TestManualPastedURLRequiresCurrentState(t *testing.T) {
	var output bytes.Buffer
	code, err := promptForAuthCode("https://example.test/auth?state=current", strings.NewReader("http://localhost:123/oauth/callback?state=stale&code=wrong\nhttp://localhost:123/oauth/callback?state=current&code=right\n"), &output)
	if err != nil || code != "right" {
		t.Fatalf("code=%s err=%v", code, err)
	}
}

type callbackOutput struct {
	t     *testing.T
	fired bool
	body  chan string
}

func (o *callbackOutput) Write(p []byte) (int, error) {
	if !o.fired {
		fields := strings.Fields(string(p))
		for _, field := range fields {
			if !strings.Contains(field, "/auth?") {
				continue
			}
			u, err := url.Parse(field)
			if err != nil {
				continue
			}
			redirect, _ := url.Parse(u.Query().Get("redirect_uri"))
			q := redirect.Query()
			q.Set("state", u.Query().Get("state"))
			q.Set("code", "callback-code")
			redirect.RawQuery = q.Encode()
			o.fired = true
			go func() {
				response, err := http.Get(redirect.String())
				if err != nil {
					o.body <- "request failed: " + err.Error()
					return
				}
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				o.body <- string(body)
			}()
			break
		}
	}
	return len(p), nil
}

func TestLoginNoBrowserAcceptsCallbackWithoutPaste(t *testing.T) {
	withTempConfigDir(t)
	a, _ := newTestAuthenticator(t, fakeTokenJSON)
	input, writer := io.Pipe()
	defer writer.Close()
	output := &callbackOutput{t: t, body: make(chan string, 1)}
	token, err := a.LoginWithOptions(context.Background(), LoginOptions{NoBrowser: true, Input: input, Output: output})
	if err != nil || token == nil || token.AccessToken != "fake-at" {
		t.Fatalf("token=%v err=%v", token, err)
	}
	if body := <-output.body; !strings.Contains(body, "still finishing") || strings.Contains(body, "Authorized!") {
		t.Fatal(body)
	}
}

func TestCallbackDoesNotClaimAuthorizationWhenExchangeFails(t *testing.T) {
	withTempConfigDir(t)
	a, _ := newTestAuthenticator(t, `{ "error": "invalid_grant" }`)
	input, writer := io.Pipe()
	defer writer.Close()
	output := &callbackOutput{t: t, body: make(chan string, 1)}
	if _, err := a.LoginWithOptions(context.Background(), LoginOptions{NoBrowser: true, Input: input, Output: output}); err == nil {
		t.Fatal("failed exchange accepted")
	}
	if body := <-output.body; strings.Contains(body, "Authorized!") || !strings.Contains(body, "check its result") {
		t.Fatal(body)
	}
}

func TestStaleDenialDoesNotEndCurrentAttempt(t *testing.T) {
	var output bytes.Buffer
	code, err := promptForAuthCode("https://example.test/auth?state=current", strings.NewReader("http://localhost:123/oauth/callback?state=old&error=access_denied\nright-code\n"), &output)
	if err != nil || code != "right-code" {
		t.Fatalf("code=%s err=%v", code, err)
	}
}

func TestCallbackAfterFailureCannotClaimCodeReceived(t *testing.T) {
	cb, cleanup, err := startCallbackServer("state")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(cb.url + "?state=state&error=access_denied")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if err := <-cb.errCh; !strings.Contains(err.Error(), "access_denied") {
		t.Fatal(err)
	}
	response, err = client.Get(cb.url + "?state=state&code=late")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || strings.Contains(string(body), "code received") {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
}

// The subprocess deliberately inherits a blocking OS stdin pipe and receives
// no line at all. io.Pipe-only tests would miss an os.Stdin Close/join deadlock.
func TestAuthStdinChild(t *testing.T) {
	if os.Getenv("VI_AUTH_STDIN_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	withTempConfigDir(t)
	a, _ := newTestAuthenticator(t, fakeTokenJSON)
	if _, err := a.LoginWithOptions(context.Background(), LoginOptions{NoBrowser: true}); err != nil {
		t.Fatal(err)
	}
	fmt.Println("AUTH_CALLBACK_COMPLETE")
}

func TestDefaultBlockingStdinCallbackNeedsNoPaste(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestAuthStdinChild$")
	cmd.Env = append(os.Environ(), "VI_AUTH_STDIN_CHILD=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	scanner := bufio.NewScanner(output)
	var authURL *url.URL
	for scanner.Scan() {
		for _, field := range strings.Fields(scanner.Text()) {
			if strings.Contains(field, "/auth?") {
				authURL, err = url.Parse(field)
				break
			}
		}
		if authURL != nil {
			break
		}
	}
	if authURL == nil || err != nil {
		t.Fatalf("no child auth URL: %v", err)
	}
	redirect, _ := url.Parse(authURL.Query().Get("redirect_uri"))
	query := redirect.Query()
	query.Set("state", authURL.Query().Get("state"))
	query.Set("code", "actual-stdin-callback")
	redirect.RawQuery = query.Encode()
	response, err := (&http.Client{Timeout: time.Second}).Get(redirect.String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("default blocking stdin prevented callback completion")
	}
}
