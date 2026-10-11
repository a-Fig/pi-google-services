// Package auth handles the OAuth 2.0 PKCE flow for Google Calendar access.
package auth

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/sombi/pi-google-services/internal/config"
)

const redirectPath = "/oauth/callback"

// ParseAuthCode extracts the authorization code from user-pasted input.
// It accepts either a full callback URL (copied from the browser address bar
// after Google redirects, even if the page failed to load) or a raw code.
func ParseAuthCode(pasted string) (string, error) {
	pasted = strings.TrimSpace(pasted)
	if pasted == "" {
		return "", fmt.Errorf("no input: paste the callback URL or the code")
	}

	// Raw code (no URL): accept as-is if it looks like one token.
	// Google auth codes never contain whitespace; anything with spaces is
	// user chatter, not a code.
	if !strings.Contains(pasted, "://") {
		if strings.ContainsAny(pasted, " \t") {
			return "", fmt.Errorf("not a valid code or URL: %q", pasted)
		}
		return pasted, nil
	}

	u, err := url.Parse(pasted)
	if err != nil {
		return "", fmt.Errorf("parse pasted URL: %w", err)
	}
	q := u.Query()
	if e := q.Get("error"); e != "" {
		desc := q.Get("error_description")
		if desc != "" {
			return "", fmt.Errorf("authorization denied (%s): %s", e, desc)
		}
		return "", fmt.Errorf("authorization denied (%s)", e)
	}
	code := q.Get("code")
	if code == "" {
		return "", fmt.Errorf("no code found in pasted URL")
	}
	return code, nil
}

// PKCEParams holds the PKCE code challenge data.
type PKCEParams struct {
	CodeVerifier  string
	CodeChallenge string
}

// GeneratePKCE creates a new PKCE challenge pair (S256 method).
func GeneratePKCE() (*PKCEParams, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("random: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(b)

	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])

	return &PKCEParams{
		CodeVerifier:  verifier,
		CodeChallenge: challenge,
	}, nil
}

// Authenticator manages OAuth2 authentication with PKCE for Google APIs.
type Authenticator struct {
	OAuthConfig *oauth2.Config
	Token       *oauth2.Token
}

// NewFromCredentials creates an Authenticator from a Google-provided credentials
// JSON file (the one you download from Google Cloud Console).
// Scopes are provided by the caller (e.g. allScopes() in main) so setup and
// login always request the same set of permissions.
func NewFromCredentials(creds *config.Credentials, scopes []string) *Authenticator {
	app := creds.AppConfig()
	return &Authenticator{
		OAuthConfig: &oauth2.Config{
			ClientID:     app.ClientID,
			ClientSecret: app.ClientSecret,
			Scopes:       scopes,
			Endpoint:     google.Endpoint,
			RedirectURL:  "http://localhost:0" + redirectPath,
		},
	}
}

func printAuthPrompt(authURL string, output io.Writer) {
	fmt.Fprintf(output, `
No browser available — authorize from any device:

  1. Open this URL (on this machine or your phone):

     %s

  2. Approve the consent screen. Google will redirect to a localhost page
     that FAILS TO LOAD — that is expected.

  3. On this machine the callback is received automatically. On another
     device, copy the full URL from the browser address bar and paste it here.

Paste the URL or code (empty line to retry, Ctrl+C to cancel):
> `, authURL)
}

// promptForAuthCode shows the auth URL with manual-mode instructions and
// reads user input until it can parse an authorization code. It re-prompts
// on unparseable input and fails immediately on a denied authorization.
func promptForAuthCode(authURL string, input io.Reader, output io.Writer) (string, error) {
	scanner := bufio.NewScanner(input)
	for {
		printAuthPrompt(authURL, output)

		if !scanner.Scan() {
			return "", fmt.Errorf("no authorization code entered")
		}
		code, err := parseAttemptCode(authURL, scanner.Text())
		if err == nil {
			return code, nil
		}
		if strings.Contains(err.Error(), "denied") {
			return "", err
		}
		fmt.Fprintf(output, "⚠ %v\n", err)
	}
}

// Check attempt binding before interpreting denial as well as successful codes.
func parseAttemptCode(authURL, pasted string) (string, error) {
	if strings.Contains(pasted, "://") {
		expected, _ := url.Parse(authURL)
		received, err := url.Parse(strings.TrimSpace(pasted))
		if err != nil {
			return "", fmt.Errorf("invalid callback URL")
		}
		if expected.Query().Get("state") != "" && received.Query().Get("state") != expected.Query().Get("state") {
			return "", fmt.Errorf("callback belongs to another sign-in attempt")
		}
	}
	return ParseAuthCode(pasted)
}

type inputLine struct {
	text string
	err  error
}

func scanInput(input io.Reader, stop <-chan struct{}) (<-chan inputLine, <-chan struct{}) {
	lines := make(chan inputLine, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(lines)
		scanner := bufio.NewScanner(input)
		for scanner.Scan() {
			select {
			case lines <- inputLine{text: scanner.Text()}:
			case <-stop:
				return
			}
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		select {
		case lines <- inputLine{err: err}:
		case <-stop:
		}
	}()
	return lines, done
}

var stdinOnce sync.Once
var stdinLines <-chan inputLine

// Blocking terminal/inherited stdin cannot reliably be interrupted by Close.
// One process-wide pump owns it; attempts never create another OS reader. The
// login CLI exits after completion, reclaiming that one blocked reader.
func processStdinLines() <-chan inputLine {
	stdinOnce.Do(func() { stdinLines, _ = scanInput(os.Stdin, nil) })
	return stdinLines
}

func waitForAuthCode(ctx context.Context, authURL string, input io.ReadCloser, output io.Writer, cb *callbackServer) (string, error) {
	var lines <-chan inputLine
	if input == nil {
		lines = processStdinLines()
	} else {
		stop := make(chan struct{})
		var done <-chan struct{}
		lines, done = scanInput(input, stop)
		defer func() { close(stop); input.Close(); <-done }()
	}
	for {
		printAuthPrompt(authURL, output)
		select {
		case answer, ok := <-lines:
			if !ok || answer.err != nil {
				return "", fmt.Errorf("no authorization code entered")
			}
			code, err := parseAttemptCode(authURL, answer.text)
			if err == nil {
				return code, nil
			}
			if strings.Contains(err.Error(), "denied") {
				return "", err
			}
			fmt.Fprintf(output, "Invalid input: %v\n", err)
		case code := <-cb.codeCh:
			return code, nil
		case err := <-cb.errCh:
			return "", fmt.Errorf("callback: %w", err)
		case <-ctx.Done():
			return "", fmt.Errorf("login cancelled: %w", ctx.Err())
		}
	}
}

// LoginOptions configures the login flow. Zero values give the classic
// behavior: try the system browser and fail loudly if unavailable.
type LoginOptions struct {
	// NoBrowser skips launching a browser; either a loopback callback or
	// a pasted authorization URL completes the flow. Use on headless hosts (SSH,
	// VPS, containers) or inside WSL when localhost forwarding breaks.
	NoBrowser bool

	// Input is where the pasted URL is read from in manual mode. It must
	// unblock Read when closed; callback/cancellation closes and joins it.
	// Finite in-memory readers can be wrapped with io.NopCloser.
	//
	// Nil uses the single process-wide stdin reader. Output receives prompts
	// (default os.Stdout).
	Input  io.ReadCloser
	Output io.Writer

	// OpenBrowser overrides the default browser launcher (tests).
	OpenBrowser func(string) error
}

func (o LoginOptions) input() io.ReadCloser {
	if o.Input != nil {
		return o.Input
	}
	return nil
}

func (o LoginOptions) output() io.Writer {
	if o.Output != nil {
		return o.Output
	}
	return os.Stdout
}

func (o LoginOptions) openBrowser() func(string) error {
	if o.OpenBrowser != nil {
		return o.OpenBrowser
	}
	return openBrowser
}

// Login performs the PKCE OAuth flow with default options.
func (a *Authenticator) Login(ctx context.Context) (*oauth2.Token, error) {
	return a.LoginWithOptions(ctx, LoginOptions{})
}

// LoginWithOptions performs the PKCE OAuth flow. It tries the loopback +
// browser flow unless opts.NoBrowser is set, and falls back to manual
// paste mode when the browser cannot be launched.
func (a *Authenticator) LoginWithOptions(ctx context.Context, opts LoginOptions) (*oauth2.Token, error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("generate pkce: %w", err)
	}

	// The callback server always runs: it allocates the real loopback port
	// that goes into the authorization URL (port 0 is not a valid
	// redirect), and catches the redirect whenever a browser is involved.
	state, err := GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}
	cb, cleanup, err := startCallbackServer(state.CodeVerifier)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	a.OAuthConfig.RedirectURL = cb.url

	authURL := a.authCodeURL(pkce, state.CodeVerifier)

	var authCode string
	switch {
	case opts.NoBrowser:
		if authCode, err = waitForAuthCode(ctx, authURL, opts.input(), opts.output(), cb); err != nil {
			return nil, err
		}
	default:
		fmt.Fprintln(opts.output(), "\n📎 Opening browser for Google authorization...")
		if openErr := opts.openBrowser()(authURL); openErr != nil {
			// Browser unavailable: degrade to manual paste instead of dying.
			fmt.Fprintln(opts.output(), "\n⚠ Could not launch a browser — switching to manual mode.")
			if authCode, err = waitForAuthCode(ctx, authURL, opts.input(), opts.output(), cb); err != nil {
				return nil, err
			}
			break
		}
		fmt.Fprintln(opts.output(), "Check your browser and authorize the application.")

		select {
		case authCode = <-cb.codeCh:
			fmt.Fprintln(opts.output(), "✓ Authorization code received, exchanging for tokens...")
		case err := <-cb.errCh:
			return nil, fmt.Errorf("callback: %w", err)
		case <-ctx.Done():
			return nil, fmt.Errorf("login cancelled")
		}
	}

	fmt.Fprintln(opts.output(), "Authorization code received, exchanging for tokens...")
	token, err := a.exchange(ctx, pkce, authCode)
	if err != nil {
		return nil, err
	}

	a.Token = token

	if err := saveOAuthToken(token); err != nil {
		log.Printf("Warning: could not save token: %v", err)
	}

	fmt.Fprintln(opts.output(), "✓ Authentication successful!")
	return token, nil
}

// authCodeURL builds the Google authorization URL with PKCE parameters.
func (a *Authenticator) authCodeURL(pkce *PKCEParams, state string) string {
	return a.OAuthConfig.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", pkce.CodeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
}

// exchange trades an authorization code for tokens using the PKCE verifier
// and persists the result to disk.
func (a *Authenticator) exchange(ctx context.Context, pkce *PKCEParams, code string) (*oauth2.Token, error) {
	token, err := a.OAuthConfig.Exchange(ctx, code,
		oauth2.SetAuthURLParam("code_verifier", pkce.CodeVerifier),
	)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	return token, nil
}

// callbackServer is the local loopback server that catches Google's
// redirect. It always runs, even in manual paste mode: it allocates the
// real port that goes into the authorization URL.
type callbackServer struct {
	server *http.Server
	url    string // full redirect URL (http://localhost:<port>/oauth/callback)
	codeCh <-chan string
	errCh  <-chan error
}

func cbURLForRequest(r *http.Request) string {
	return "http://localhost" + r.URL.RequestURI()
}

// startCallbackServer binds a loopback listener and serves the OAuth
// callback handler on a random port. Call cleanup when done.
func startCallbackServer(state string) (*callbackServer, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("listen: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	var once sync.Once
	var resultFailed bool
	var resultMu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc(redirectPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.URL.Query().Get("state") != state {
			http.Error(w, "This callback belongs to another sign-in attempt. Return to your current sign-in page.", http.StatusBadRequest)
			return
		}
		resultMu.Lock()
		defer resultMu.Unlock()
		code, callbackErr := ParseAuthCode(cbURLForRequest(r))
		if callbackErr != nil {
			once.Do(func() { resultFailed = true; errCh <- callbackErr })
			http.Error(w, "Google sign-in was not completed. Return to setup to try again.", http.StatusBadRequest)
			return
		}
		if resultFailed {
			http.Error(w, "This sign-in attempt has already failed. Return to setup to try again.", http.StatusBadRequest)
			return
		}
		once.Do(func() { codeCh <- code })
		fmt.Fprint(w, "Sign-in code received. The connection is still finishing; return to setup or the terminal to check its result.")
	})

	server := &http.Server{Handler: mux}
	go server.Serve(listener) //nolint:errcheck // listener already bound

	cleanup := func() {
		server.Close()
		listener.Close()
	}

	return &callbackServer{
		server: server,
		url:    fmt.Sprintf("http://localhost:%d%s", port, redirectPath),
		codeCh: codeCh,
		errCh:  errCh,
	}, cleanup, nil
}

// TokenSource returns a TokenSource that refreshes on startup and persists refreshed tokens.
// The old implementation refreshed only in memory. After a long-running server refreshed its
// token, a restarted server could reload the stale access token from disk and receive 401s.
func (a *Authenticator) TokenSource(ctx context.Context, token *oauth2.Token) oauth2.TokenSource {
	startupToken := new(oauth2.Token)
	*startupToken = *token
	if startupToken.RefreshToken != "" {
		// Force one refresh per server start. This avoids trusting a stale or revoked access token
		// whose persisted expiry still appears valid.
		startupToken.Expiry = time.Now().Add(-time.Hour)
	}
	return &persistingTokenSource{
		source: a.OAuthConfig.TokenSource(ctx, startupToken),
		save:   saveOAuthToken,
	}
}

// CachedTokenSource reuses a valid token loaded from disk and persists only
// genuine refreshes. Short-lived polling helpers should use this instead of
// forcing a refresh every time they start.
func (a *Authenticator) CachedTokenSource(ctx context.Context, token *oauth2.Token) oauth2.TokenSource {
	return &persistingTokenSource{
		source:          a.OAuthConfig.TokenSource(ctx, token),
		save:            saveOAuthToken,
		lastAccessToken: token.AccessToken,
	}
}

type persistingTokenSource struct {
	source          oauth2.TokenSource
	save            func(*oauth2.Token) error
	lastAccessToken string
}

func (s *persistingTokenSource) Token() (*oauth2.Token, error) {
	token, err := s.source.Token()
	if err != nil {
		return nil, err
	}
	if token.AccessToken != s.lastAccessToken {
		if err := s.save(token); err != nil {
			log.Printf("Warning: could not persist refreshed token: %v", err)
		}
		s.lastAccessToken = token.AccessToken
	}
	return token, nil
}

// saveOAuthToken persists the token to the config directory.
func saveOAuthToken(token *oauth2.Token) error {
	t := &config.Tokens{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    token.TokenType,
		Expiry:       token.Expiry.Format(time.RFC3339),
	}
	if err := config.SaveTokens(t); err != nil {
		return fmt.Errorf("save tokens: %w", err)
	}
	return nil
}

// LoadToken reads a previously stored token from disk.
func LoadToken() (*oauth2.Token, error) {
	t, err := config.LoadTokens()
	if err != nil {
		return nil, err
	}
	if t.AccessToken == "" {
		return nil, nil
	}
	expiry, _ := time.Parse(time.RFC3339, t.Expiry)
	return &oauth2.Token{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		TokenType:    t.TokenType,
		Expiry:       expiry,
	}, nil
}

// HasToken returns true if a stored token exists on disk.
func HasToken() bool {
	t, err := LoadToken()
	return err == nil && t != nil
}

// openBrowser opens a URL in the default system browser.
func openBrowser(url string) error {
	// Try xdg-open first (Linux with desktop env)
	if err := exec.Command("xdg-open", url).Start(); err == nil {
		return nil
	}
	// macOS
	if err := exec.Command("open", url).Start(); err == nil {
		return nil
	}
	// Windows
	if err := exec.Command("cmd", "/c", "start", url).Start(); err == nil {
		return nil
	}
	// Fallback: try common browsers
	for _, browser := range []string{"x-www-browser", "firefox", "google-chrome", "chromium", "brave"} {
		if err := exec.Command(browser, url).Start(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("no browser found; open manually: %s", url)
}
