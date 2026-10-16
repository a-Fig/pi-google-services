# Changelog

## Unreleased

- **Receiving attachments**: `get-email` now lists the attachments on a message — filename, MIME type, size, inline flag and the attachment ID needed to fetch it.
- **New tool `download-attachment`**: saves an attachment from a received email to disk by message ID + attachment ID. `savePath` accepts a directory (saves under the original filename), a full file path, or is omitted for the system temp directory.
- Handles both storage models Gmail uses: large attachments fetched via `users.messages.attachments.get`, and small ones whose bytes ride inline in the MIME part (addressed by part ID).
- Sender-supplied filenames are reduced to a single path element before being joined to a directory, so an attachment cannot be written outside the chosen destination.
- Base64 decoding now tolerates unpadded base64url payloads, which also makes body extraction more robust.
- Attachment sizes are rendered with the existing `fmtSize` helper shared with the Drive tools, which also covers GB.
- 13 new unit tests (attachment extraction, part lookup, base64 decoding, save-path resolution, filename sanitization, display formatting).
- **Replies actually thread** — `reply-to-email` now sends `In-Reply-To` and `References` derived from the thread, and reuses the thread's subject. Previously it set only `ThreadId`, which Gmail's own UI honours but every other mail client ignores, so recipients saw a disconnected new email. `to` and `subject` are now optional.


## v0.1.20-afig.1

- **Calendar event colors** — `create-event` and `update-event` accept `colorId`.
- **Shared-calendar diagnostics** — `list-calendars` reports access roles and calendar colors; Google API error details are visible in the main MCP error message.
- **Safer event updates** — updates use PATCH so omitted fields are preserved.
- **Multi-calendar search** — `search-events` accepts `calendarId`.
- **Actionable event results** — list/search output includes event ID, calendar ID, and color ID.
- **Reliable OAuth restarts** — refreshed access tokens are persisted, and server startup refreshes once instead of trusting a stale token file that can produce 401s.

## v0.1.19

- **Feature: headless login (`--no-browser`)** — `login` and `setup` now accept `--no-browser` for machines without a browser (SSH, VPS, containers, WSL with broken localhost forwarding). The tool prints the authorization URL; you open it on any device (phone included), approve, and paste back the redirected URL. PKCE is preserved end-to-end.
- **Fix: automatic manual-mode fallback** — when no browser can be launched, the flow degrades to the manual paste prompt instead of printing a dead URL and hanging.
- **Fix: authorization URL now carries a real loopback port** — the callback server always starts before building the auth URL. Previously, in flows without a listener the URL contained `redirect_uri=http://localhost:0/...`, which Google can reject.
- **Refactor: `Authenticator.LoginWithOptions(opts)`** — browser and manual flows share one code path (single callback server, single exchange). `Login()` keeps its signature as the default-options wrapper. Input/output are injectable for testing.

## v0.1.18

- **Fix: `setup` now requests all 5 service scopes** — previously it only asked Google for Calendar permissions (hardcoded in `NewFromCredentials`), so Gmail/Tasks/Drive/Contacts failed after following the official `setup` flow.
- **Refactor: single source of truth for OAuth scopes** — `login`, `setup` and `serve` now share one `newAuthenticator()` that always derives scopes from `allScopes()` (the registered services). `serve` no longer trusts scopes persisted in `config.json`, which could drift out of sync. The redundant `config.json` persistence (`Config`/`Load`/`Save`) was removed — client credentials live only in `credentials.json`, tokens in `tokens.json`.
- **Refactor: `config.Credentials.AppConfig()`** — the Installed→Web fallback moved from inside the auth constructor to the data type that owns it.

## v0.1.17

- **Fix: silent MCP config skip in install.js** — `~/.pi/agent/mcp.json` is now created (with `mkdir -p`) when missing, instead of being skipped with a warning while install still reported success. Install now fails with a clear error if the MCP config can't be resolved.
- **Fix: install no longer depends on a fragile GitHub download** — the npm tarball already ships the platform binaries and `credentials.json`, so `install.js` unpacks from the local package. GitHub Releases is only a fallback for dev checkouts. This removes the postinstall network dependency entirely.
- **Fix: self-healing Pi extension** — if npm's `allowScripts` blocked the postinstall (Pi's default), the extension installs the missing binary on session start via `pi.exec()` instead of silently failing. The extension also now dispatches `/mcp reconnect` correctly with `expandPromptTemplates: true` (it was being sent to the model as plain text).
- **Fix: detect missing pi-mcp-adapter** — the extension checks whether `/mcp` is available and guides the user to install `pi-mcp-adapter` instead of sending a command that doesn't exist. Documented as a requirement in the README.
- **Fix: credentials.json validation in CI** — `bundle-credentials` now validates the JSON with `jq` and fails the build on corruption, instead of publishing a broken asset. Re-save the `GOOGLE_OAUTH_CREDENTIALS_JSON` secret as single-line JSON (the previous value had line-wrapping inside string literals).
- **Fix: SKILL.md missing frontmatter** — added the `description` field required by Pi's skill toolchain.

## v0.1.16

- **Fix: install.js redirect handling** — GitHub releases return HTTP 302 redirects, but the download function used `res.location` (non-existent) instead of `res.headers.location`. Redirects were never followed, causing every install/update to fail with "HTTP 302" error.

## v0.1.15

- **Fix: install.js URL doubling** — `REPO` already contains the full GitHub URL, so prepending `https://github.com/` produced `https://github.com/https://github.com/...` (404). This was the root cause of install/update always failing — the binary was never downloaded, always falling back to whatever was in `~/.local/bin/`.

## v0.1.14

- **Fix: install.js platform mapping** — `x64` now correctly maps to `amd64` to match GitHub Release asset names (Go's `GOARCH` nomenclature). Previously, Linux x64 users couldn't download the binary during install/update.

## v0.1.13

- **Email attachments**: `send-email` and `reply-to-email` now accept an optional `attachments` array
- Attach from local file paths (`localPath`) or Google Drive file IDs (`driveFileId`)
- MIME `multipart/mixed` encoding with base64-wrapped attachment data
- 9 new unit tests (MIME multipart, attachment resolution, edge cases)

## v0.1.0

- Initial release
- Google Calendar: list, create, update, delete, search events
- Gmail: list inbox, read, send, reply, search
- OAuth2 PKCE login with embedded credentials
- Pi MCP integration via package install
