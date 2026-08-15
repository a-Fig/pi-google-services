# Changelog

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
