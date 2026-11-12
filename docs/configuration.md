# Configuration

Everything is set with environment variables, read once at start-up. An invalid value stops the
server with a message that names the variable (and never prints a secret). `.env.example` is a
ready-to-copy template.

## Connection

| Variable | Default | Meaning |
|---|---|---|
| `NEXTCLOUD_URL` | required | Base URL of Nextcloud, `https://…`. No credentials, query or fragment. |
| `NEXTCLOUD_USER` | required | The user **id** (as in WebDAV URLs), not an e-mail alias. |
| `NEXTCLOUD_APP_PASSWORD` | required | An app password (Settings → Security → Devices & sessions). |
| `NEXTCLOUD_ALLOW_INSECURE_HTTP` | `false` | Accept an `http://` URL. Only for local testing. |

TLS certificates are always verified and there is no switch to turn that off. For a private
certificate authority, add it to the system trust store or point `SSL_CERT_FILE` at it (standard
Go behaviour).

## Permissions

All off by default: the server is read-only.

| Variable | Enables |
|---|---|
| `NEXTCLOUD_ALLOW_WRITE` | Creating and editing files and spreadsheets, folders, move, copy, uploads, restoring from trash or versions, tags, favorites, comments |
| `NEXTCLOUD_ALLOW_DELETE` | `nc_delete` (to the trash bin only) |
| `NEXTCLOUD_ALLOW_SHARE` | The `sharing` tools (the group must be enabled too) |

A tool whose permission is off is not registered: clients do not see it and cannot call it.

## Scope

| Variable | Default | Meaning |
|---|---|---|
| `NEXTCLOUD_ROOT` | empty (whole account) | The only folder the tools can see. Every path is relative to it. |
| `NEXTCLOUD_FOLDER_WRITE_PREFIX` | empty (disabled) | Folder, relative to the root, inside which `nc_create_folder` may create folders. |
| `NEXTCLOUD_EDITABLE_EXTENSIONS` | `.md,.txt,.json,.csv,.yml,.yaml,.xml,.drawio,.dio` | Text files that may be created or edited. |
| `NEXTCLOUD_EDITABLE_SPREADSHEETS` | empty (none) | Glob patterns of the `.xlsx` files that may be modified. |
| `NEXTCLOUD_PROTECTED_SHEETS` | empty | Sheet names that are never written to. |

Spreadsheet patterns are comma-separated and case-insensitive. A pattern without `/` is matched
against the file name (`Decisions*.xlsx`); a pattern with `/` against the path relative to the
root (`Plans/*/matrix.xlsx`). `*` does not cross a `/`.

## Tool groups

`NEXTCLOUD_TOOL_GROUPS` is a comma-separated list. Default:
`files,sheets,safe_ops,binary,metadata,diagnostics`. The `sharing` group is never on unless
listed explicitly. See [tools.md](tools.md) for every tool.

## Sharing policy

| Variable | Default | Meaning |
|---|---|---|
| `NEXTCLOUD_SHARE_REQUIRE_PASSWORD` | `true` | Public links must have a password (at least 10 characters). |
| `NEXTCLOUD_SHARE_REQUIRE_EXPIRY` | `true` | Public links must have an expiry date. |
| `NEXTCLOUD_SHARE_MAX_EXPIRY_DAYS` | `30` | Furthest allowed expiry date, for any share. |

## Server

| Variable | Default | Meaning |
|---|---|---|
| `MCP_TRANSPORT` | `stdio` | `stdio` or `streamable-http`. |
| `MCP_HTTP_ADDR` | `:8000` | Listen address for HTTP. The endpoint is `/mcp`; `/healthz` answers `ok`. |
| `GIT_COMMIT` | — | Commit shown by `get_server_info` when it was not baked in at build time. |

The HTTP endpoint is stateless and **does not authenticate callers**. Put a reverse proxy in
front of it: see [`deploy/`](../deploy) for an nginx example with a Bearer token.

## Limits

| Variable | Default | Meaning |
|---|---|---|
| `NEXTCLOUD_MAX_DOWNLOAD_BYTES` | 25 MiB | Largest file read into memory (documents, images, spreadsheets). |
| `NEXTCLOUD_MAX_TEXT_BYTES` | 2 MiB | Largest text file that can be created or edited. |
| `NEXTCLOUD_MAX_EMBED_BYTES` | 2 MiB | Largest file `nc_download` returns in one piece. |
| `NEXTCLOUD_MAX_CHUNK_BYTES` | 1 MiB | Largest piece of a chunked download or upload. |
| `NEXTCLOUD_MAX_UPLOAD_BYTES` | 100 MiB | Largest file that can be uploaded. |
