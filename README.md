# cirrus-nextcloud-mcp

A standalone, reusable [MCP](https://modelcontextprotocol.io) server for Nextcloud, written in Go.
It gives an assistant controlled access to the files of one Nextcloud account over `stdio` or
streamable HTTP.

> **Status:** every tool group is implemented. Full documentation
> (reverse proxy example, Compose file, Spanish section) comes with the packaging phase.

## Safety model

- **Read-only by default.** Writing, deleting and sharing are three separate switches
  (`NEXTCLOUD_ALLOW_WRITE`, `NEXTCLOUD_ALLOW_DELETE`, `NEXTCLOUD_ALLOW_SHARE`). A tool whose
  permission is off is not registered at all, so the model never sees it.
- **Nothing is hard-coded.** The visible folder, where folders may be created, which text
  extensions and which spreadsheets are editable are all configuration.
- **No blind overwrites.** An existing file is only replaced with `If-Match` and its ETag; if it
  changed since it was read, the save fails with a clear message. New files are created with
  `If-None-Match: *`.
- Paths containing `..` are rejected, redirects are never followed, TLS verification cannot be
  turned off, and the app password never appears in logs or errors.
- The server does **not** authenticate MCP clients. Run the HTTP transport behind a reverse
  proxy that does.

## Quick start

```sh
cp .env.example .env        # fill in NEXTCLOUD_URL, NEXTCLOUD_USER, NEXTCLOUD_APP_PASSWORD
make build
set -a && . ./.env && set +a
./bin/cirrus-nextcloud-mcp  # stdio; set MCP_TRANSPORT=streamable-http to serve http://:8000/mcp
```

Every setting is described in [`.env.example`](.env.example).

## Tools: `files` group

| Tool | Needs | Purpose |
|---|---|---|
| `nc_list_files` | read | List a folder |
| `nc_search_files` | read | Search by name |
| `nc_get_file_info` | read | Id, ETag, size, dates, permissions |
| `nc_read_file` | read | Text of PDF, Word, Excel, draw.io and text files, paged |
| `nc_read_lines` | read | Numbered lines, paged, with a content hash |
| `nc_resolve_link` | read | Turn a `/f/<id>` link into a path |
| `nc_create_file` | write | Create a text file (never replaces one) |
| `nc_edit_file` | write | Replace an exact piece of text |
| `nc_edit_lines` | write | Replace a range of lines |
| `nc_create_folder` | write + `NEXTCLOUD_FOLDER_WRITE_PREFIX` | Create folders under the allowed prefix |

## Tools: `sheets` group

Spreadsheets are treated as tables: row 1 is the header, every other row is a record.

| Tool | Needs | Purpose |
|---|---|---|
| `nc_sheet_read` | read | Rows as `{header: value}`, headers, sheet names and the next free id |
| `nc_sheet_append_rows` | write + pattern | Add records, copying the format of the last one |
| `nc_sheet_update_cells` | write + pattern | Change cells of one record, optionally checking expected values |
| `nc_sheet_create` | write + pattern | Create a workbook, or add a sheet to an existing one |

"Pattern" means the file must match `NEXTCLOUD_EDITABLE_SPREADSHEETS` (for example
`Decisions*.xlsx,Plans/*/matrix.xlsx`); with no pattern, no spreadsheet can be modified. Sheets
listed in `NEXTCLOUD_PROTECTED_SHEETS` are never written to. Every save uses `If-Match` and is
read back afterwards (`"verified"`).

Known limits: growing an Excel table (`ListObject`) on append recreates it with the same name,
range and style, so totals rows and calculated columns of that table are not preserved; a
sheet-level autofilter is extended only when the workbook declares it as a defined name.

## Tools: `safe_ops` group

| Tool | Needs | Purpose |
|---|---|---|
| `nc_move` | write | Move or rename; fails if the destination exists |
| `nc_copy` | write | Copy; fails if the destination exists |
| `nc_delete` | delete | Move to the trash bin (refused when Nextcloud has no trash bin) |
| `nc_trash_list` | read | Deleted items that can be restored |
| `nc_trash_restore` | write | Put a deleted item back |
| `nc_versions_list` | read | Previous versions of a file |
| `nc_versions_restore` | write | Make a previous version current, if the file's ETag still matches |

There is no tool that deletes permanently or empties the trash. Only trash items that were
inside the configured root are listed or restorable.

Known limits: Nextcloud itself bypasses the trash for files larger than the space the trash
may use; `nc_delete` reports when the item cannot be found in the trash afterwards. Restoring a
version is checked against the ETag just before the request, not atomically with it.

## Tools: `binary` group

| Tool | Needs | Purpose |
|---|---|---|
| `nc_download` | read | Any file as an embedded resource; large files in parts with `offset`/`length` |
| `nc_upload_start` | write | Open an upload for a new file |
| `nc_upload_chunk` | write | Send one base64 piece |
| `nc_upload_finish` | write | Join the pieces; optional SHA-256 check |
| `nc_read_image` | read | An image as image content, scaled down and turned upright |
| `nc_render_pdf` | read + `pdftoppm` | PDF pages as images (at most 5 per call) |

Binary data is returned as MCP resource or image content, never as base64 inside the text the
model reads. Uploads only create files: an existing path is refused when starting and again by
Nextcloud itself when finishing (`Overwrite: F`). The upload state lives in Nextcloud's own
chunked-upload area, so the server stays stateless. `nc_render_pdf` needs `pdftoppm`
(poppler-utils, included in the Docker image); without it the tool is not registered.
Limits: `NEXTCLOUD_MAX_EMBED_BYTES`, `NEXTCLOUD_MAX_CHUNK_BYTES`, `NEXTCLOUD_MAX_UPLOAD_BYTES`.

## Tools: `metadata` group

| Tool | Needs | Purpose |
|---|---|---|
| `nc_tags_list` | read | System tags, or the tags of one file |
| `nc_tag_search` | read | Files carrying a tag |
| `nc_tag_create`, `nc_tag_assign`, `nc_tag_remove` | write | Manage tags on files |
| `nc_favorites_list` | read | Favorite files |
| `nc_favorite_set` | write | Mark or unmark a favorite |
| `nc_comments_list` | read | Comments on a file |
| `nc_comment_add` | write | Add a comment |
| `nc_activity_list` | read | Recent activity (needs the Activity app) |
| `nc_recent_files` | read | Files modified in the last days |
| `nc_user_info` | read | The connected account and its quota |

With `NEXTCLOUD_ROOT` set, tag and favorite searches, recent files and activity only return
items inside it; activity that is not about a file is left out.

## Tools: `sharing` group (off by default)

Needs both `sharing` in `NEXTCLOUD_TOOL_GROUPS` and `NEXTCLOUD_ALLOW_SHARE=true`.

| Tool | Purpose |
|---|---|
| `nc_shares_list` | Shares created by the account, inside the root |
| `nc_share_create` | A public link, or a share with a Nextcloud user |
| `nc_share_delete` | Stop sharing (the file is not touched) |

Public links require a password (at least 10 characters) and an expiry date at most
`NEXTCLOUD_SHARE_MAX_EXPIRY_DAYS` (30) days ahead, unless the operator relaxes
`NEXTCLOUD_SHARE_REQUIRE_PASSWORD` / `NEXTCLOUD_SHARE_REQUIRE_EXPIRY`. Shares are read-only unless
`permissions="edit"`; the right to re-share is never granted. Passwords and their hashes are
never returned.

## Tools: `diagnostics` group

`get_server_info` returns the version and commit, start time, enabled groups, the tools that
exist and the ones that are switched off (with the reason), and configuration flags. It never
returns credentials, the Nextcloud address, the account name or any path. With
`check_connection=true` it also reports whether Nextcloud answers and its version.

## Development

```sh
make check         # golangci-lint + unit tests (in-memory fake WebDAV server)
make integration   # starts Nextcloud in Docker and runs the integration tests against it
```

## License

[MIT](LICENSE)
