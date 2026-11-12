# cirrus-nextcloud-mcp

A standalone, reusable [MCP](https://modelcontextprotocol.io) server for Nextcloud, written in Go.
It gives an assistant controlled access to the files of one Nextcloud account over `stdio` or
streamable HTTP.

> **Status:** under construction. The `files` and `sheets` groups are implemented; safe file
> operations, binaries, metadata and sharing arrive in the next phases. Full documentation
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

## Development

```sh
make check         # golangci-lint + unit tests (in-memory fake WebDAV server)
make integration   # starts Nextcloud in Docker and runs the integration tests against it
```

## License

[MIT](LICENSE)
