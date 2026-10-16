# Email reading fixtures

`testdata/reading.json` contains synthetic Gmail API messages: a CSS-heavy HTML receipt,
a marketing newsletter, HTML entities, a long Unicode email, an eleven-message conversation,
and literal attachment-marker text alongside a real attachment appendix. No account is used.
The fake HTTP transport in `email_reading_http_test.go` handles every Gmail request locally.

`go test -p 1 ./...` covers HTML cleanup, exact body reconstruction, ordered conversation
pages and insertion/removal between pages. `get-email` retains its default text format;
`format=json` adds metadata, 5000 Unicode-character body pages and `nextOffset`.
`get-thread` returns five chronological messages, with an ID tie-break for equal dates.
Its opaque `nextCursor` binds the offset to the ordered message IDs. A changed thread returns
an empty page with `threadChanged=true` and a restart note; it never slices a changed list
at an old position. This cursor needs no process-local cache and survives connector restarts.

To capture actual handler results for the companion daemon's before/after example script,
set `EMAIL_READING_CAPTURE` to a new JSON output path and run
`go test -p 1 ./internal/services -run '^TestReadingCapture$' -count=1`.
The same capture test runs against the shipped `email-watch` revision `9d501dd` (before)
and this branch (after). The before checkout needs only the test transport option on
`gmail.New`; its message handler and body stripping/truncation stay unchanged.
The companion repo keeps matching input fixtures and both captures in its tests.

On the Windows laptop run Go tests with scratch `HOME` and `USERPROFILE`, outside the OS
temp directory, and create `Downloads` there. This protects real credentials and preserves
the attachment-path tests' assumptions. No phone, APK or real Gmail acceptance is covered.
