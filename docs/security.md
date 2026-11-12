# Security model

## What the server guarantees

| Guarantee | How |
|---|---|
| Read-only unless told otherwise | Write, delete and share are three separate switches, all off by default. A tool without its permission is never registered. |
| Confined to one folder | Every path is joined under `NEXTCLOUD_ROOT`; `..` and control characters are rejected before any request. Searches, tag and favorite filters, the trash, activity and shares are filtered to the root too. Links (`/f/<id>`) are resolved with a search scoped to the root. |
| No blind overwrite | The only way to replace a file is a `PUT` with `If-Match` and the ETag read just before; the client refuses to send any other `PUT`. New files use `If-None-Match: *`. Move, copy and upload assembly send `Overwrite: F`. |
| No permanent deletion | `nc_delete` first asks Nextcloud (uncached) whether the trash bin is enabled and refuses otherwise. There is no tool to empty the trash or delete a version. |
| Credentials stay put | The app password is sent only in the `Authorization` header to the configured host. Redirects are never followed, so it cannot be replayed elsewhere. It is a `Secret` type that prints as `[redacted]`, and every error text is scrubbed before it is logged or returned. |
| TLS always verified | There is no option to skip certificate verification. Plain `http://` needs an explicit opt-in meant for local tests. |
| No shared state | No cookies, no sessions, no cache of file content. The HTTP transport is stateless; uploads keep their state in Nextcloud. |
| Sharing is opt-in twice | The `sharing` group must be listed **and** `NEXTCLOUD_ALLOW_SHARE` set. Public links need a password and a short expiry by default; re-share permission is never granted; passwords and hashes are never returned. |
| Text is never a formula | Spreadsheet values starting with `=` are stored as text. |

These properties are covered by tests: the in-memory fake counts any `PUT`, `MOVE` or `COPY`
that would overwrite without a precondition and any permanent delete, and the suites assert both
counters stay at zero.

## What it does not do

- **It does not authenticate MCP clients.** Anyone who can reach the HTTP endpoint acts with the
  configured Nextcloud account. Run it over stdio, or behind a proxy that authenticates (see
  [`deploy/`](../deploy)); never publish port 8000 directly.
- **It is not a sandbox against the account's own rights.** The server can do at most what the
  Nextcloud account can do. Use a dedicated account that only has the folders it needs, and an
  app password that can be revoked on its own.
- **Prompt injection.** File contents, comments and file names are returned to the model as
  data. A document can contain text that tries to steer the model; keep write, delete and share
  off unless needed, and prefer a narrow `NEXTCLOUD_ROOT`.

## Known limits

- Nextcloud bypasses the trash for files larger than the space the trash may use. `nc_delete`
  looks the item up in the trash afterwards and says so when it is not there.
- `nc_versions_restore` compares the ETag just before the request; the check and the restore are
  not atomic. The replaced content is kept by Nextcloud as another version.
- A co-editing session (Nextcloud Office, OnlyOffice) can write its own copy over a saved change
  after the fact. The spreadsheet tools warn about it in their result.
- `nc_render_pdf` runs `pdftoppm` on documents from Nextcloud. It is invoked without a shell,
  with a fixed argument list, a private temporary folder and a timeout; the container image runs
  as an unprivileged user with a read-only root file system in the example deployment.
- Appending to a sheet that contains an Excel table recreates the table definition (name, range
  and style are kept; totals rows and calculated columns are not).

## Reporting a vulnerability

Please open a private security advisory on the GitHub repository rather than a public issue.
