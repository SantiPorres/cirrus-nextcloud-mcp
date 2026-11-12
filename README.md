# cirrus-nextcloud-mcp

A standalone, reusable [MCP](https://modelcontextprotocol.io) server for Nextcloud, written in
Go. It gives an assistant controlled access to one Nextcloud account: browsing and reading
documents, editing text files and spreadsheets, moving and restoring files, images and PDF
pages, tags, comments and (if you ask for it) sharing.

- Transports: `stdio` and stateless streamable HTTP.
- One static binary, or a small container image that runs as an unprivileged user.
- **Read-only by default.** Nothing is hard-coded to one installation.

[Español](#español) · [Configuration](docs/configuration.md) · [Tool reference](docs/tools.md) ·
[Security model](docs/security.md)

## Safety model

- **Three separate permissions**, all off by default: `NEXTCLOUD_ALLOW_WRITE`,
  `NEXTCLOUD_ALLOW_DELETE`, `NEXTCLOUD_ALLOW_SHARE`. A tool whose permission is off is not
  registered, so the model never sees it.
- **Confined to one folder** (`NEXTCLOUD_ROOT`). Paths with `..` are rejected; searches, trash,
  activity and shares are filtered to that folder too.
- **No blind overwrites.** An existing file is only replaced with `If-Match` and its ETag; if it
  changed since it was read, the save fails with a clear message. New files, moves, copies and
  uploads never replace anything.
- **No permanent deletion.** Deleting means moving to the Nextcloud trash bin; without a trash
  bin the delete is refused.
- Redirects are never followed, TLS verification cannot be turned off, and the app password
  never appears in logs or errors.
- The server does **not** authenticate MCP clients: run the HTTP transport behind a proxy that
  does. An nginx example is included.

Details and known limits: [docs/security.md](docs/security.md).

## Quick start

Create an app password in Nextcloud (Settings → Security → Devices & sessions), then:

```sh
cp .env.example .env        # set NEXTCLOUD_URL, NEXTCLOUD_USER, NEXTCLOUD_APP_PASSWORD
make build                  # needs Go 1.27.4 or newer
set -a && . ./.env && set +a
./bin/cirrus-nextcloud-mcp  # speaks MCP on stdin/stdout
```

### With a desktop MCP client (stdio)

```json
{
  "mcpServers": {
    "nextcloud": {
      "command": "/path/to/cirrus-nextcloud-mcp",
      "env": {
        "NEXTCLOUD_URL": "https://cloud.example.com",
        "NEXTCLOUD_USER": "alice",
        "NEXTCLOUD_APP_PASSWORD": "xxxxx-xxxxx-xxxxx-xxxxx-xxxxx",
        "NEXTCLOUD_ROOT": "/Projects"
      }
    }
  }
}
```

### With Docker

```sh
docker build --build-arg GIT_COMMIT=$(git rev-parse HEAD) -t cirrus-nextcloud-mcp .
docker run --rm --env-file .env -p 127.0.0.1:8000:8000 cirrus-nextcloud-mcp
```

The image serves streamable HTTP at `http://127.0.0.1:8000/mcp`. It does not check who is
calling: keep it on localhost or put a proxy in front.

### Behind nginx with a Bearer token

[`deploy/`](deploy) has a Compose file and an nginx template. The MCP container publishes no
port; nginx terminates TLS, answers `401` unless the request carries
`Authorization: Bearer <token>`, and strips that header before forwarding.

```sh
cd deploy
cp ../.env.example .env
echo "MCP_BEARER_TOKEN=$(openssl rand -hex 32)" >> .env
mkdir certs    # fullchain.pem and privkey.pem
GIT_COMMIT=$(git rev-parse HEAD) docker compose -f docker-compose.example.yml up -d --build
```

Clients connect to `https://<host>:8443/mcp` with that header.

## Tools

43 tools in 7 groups. Each group is switched with `NEXTCLOUD_TOOL_GROUPS`; inside a group, tools
that change data need the matching permission. Parameters are in
[docs/tools.md](docs/tools.md), which is generated from the server itself.

| Group | Read | Needs a permission |
|---|---|---|
| `files` | `nc_list_files`, `nc_search_files`, `nc_get_file_info`, `nc_read_file`, `nc_read_lines`, `nc_resolve_link` | write: `nc_create_file`, `nc_edit_file`, `nc_edit_lines`, `nc_create_folder` |
| `sheets` | `nc_sheet_read` | write: `nc_sheet_append_rows`, `nc_sheet_update_cells`, `nc_sheet_create` |
| `safe_ops` | `nc_trash_list`, `nc_versions_list` | write: `nc_move`, `nc_copy`, `nc_trash_restore`, `nc_versions_restore` · delete: `nc_delete` |
| `binary` | `nc_download`, `nc_read_image`, `nc_render_pdf` | write: `nc_upload_start`, `nc_upload_chunk`, `nc_upload_finish` |
| `metadata` | `nc_tags_list`, `nc_tag_search`, `nc_favorites_list`, `nc_comments_list`, `nc_activity_list`, `nc_recent_files`, `nc_user_info` | write: `nc_tag_create`, `nc_tag_assign`, `nc_tag_remove`, `nc_favorite_set`, `nc_comment_add` |
| `sharing` (off by default) | — | share: `nc_shares_list`, `nc_share_create`, `nc_share_delete` |
| `diagnostics` | `get_server_info` | — |

Some tools need more than a permission:

- `nc_create_folder` needs `NEXTCLOUD_FOLDER_WRITE_PREFIX` and only creates folders below it.
- Text files can be created or edited only with the extensions in `NEXTCLOUD_EDITABLE_EXTENSIONS`.
- The spreadsheet write tools need `NEXTCLOUD_EDITABLE_SPREADSHEETS` (glob patterns such as
  `Decisions*.xlsx,Plans/*/matrix.xlsx`); sheets in `NEXTCLOUD_PROTECTED_SHEETS` are never written.
- `nc_render_pdf` needs `pdftoppm` (poppler-utils; included in the Docker image).
- `nc_activity_list` needs the Activity app.

What the groups do, briefly:

- **files** reads PDF, Word, Excel, draw.io and text with paging, and edits text by exact
  replacement or by line range. JSON and XML results are validated before saving.
- **sheets** treats a sheet as a table (row 1 is the header): reads rows as `{header: value}`
  with the next free id, appends rows copying the format of the last one, updates cells only if
  the expected values still hold, and reads the file back after every save.
- **safe_ops** moves, copies, deletes to the trash, and restores from the trash or from
  versions.
- **binary** returns files as embedded resources (in parts for large ones), uploads new files
  in chunks, and returns images and PDF pages as image content — never as base64 in the text the
  model reads.
- **metadata** covers system tags, favorites, comments, activity, recent files and the account's
  quota.
- **sharing** creates public links (password and expiry required by default) or shares with a
  user.
- **diagnostics** reports version, commit, active tools and why the others are off, without
  secrets, host names or paths.

## Development

```sh
make check         # golangci-lint + unit tests against an in-memory fake Nextcloud
make integration   # starts Nextcloud in Docker and runs the integration tests
make vuln          # govulncheck
```

- Unit tests drive every tool through a real MCP session against `internal/testutil/fakedav`,
  which fails the suite if a request would overwrite or permanently delete anything.
- Integration tests (`test/integration`, build tag `integration`) run the same tools against the
  Nextcloud pinned in `test/integration/docker-compose.test.yml`.
- `docs/tools.md` is generated: `UPDATE_DOCS=1 go test ./internal/server -run TestToolReference`.
- Dependencies are pinned: Go modules by `go.sum`, container images by digest, GitHub Actions by
  commit.

Layout: `cmd/` (entry point), `internal/config`, `internal/nextcloud` (WebDAV and OCS client),
`internal/tools/*` (one package per group), `internal/extract`, `internal/textedit`,
`internal/sheets`, `internal/imaging`, `internal/pdfrender`, `internal/server`.

## Roadmap

A multi-user mode, where each client sends its own Nextcloud credentials per request and the
server keeps no shared state, is described in [docs/multi-user-plan.md](docs/multi-user-plan.md).
It is not implemented.

## Español

`cirrus-nextcloud-mcp` es un servidor MCP para Nextcloud, independiente y reutilizable, escrito
en Go. Funciona por `stdio` o por HTTP (streamable, sin estado).

- **Solo lectura por defecto.** Escribir, borrar y compartir son tres permisos separados
  (`NEXTCLOUD_ALLOW_WRITE`, `NEXTCLOUD_ALLOW_DELETE`, `NEXTCLOUD_ALLOW_SHARE`). Una herramienta
  sin permiso no se registra: el modelo ni la ve.
- **Nada fijo:** la carpeta visible (`NEXTCLOUD_ROOT`), dónde se pueden crear carpetas, qué
  extensiones de texto y qué hojas de cálculo se pueden editar son configuración.
- **Sin sobrescrituras a ciegas:** un archivo existente solo se reemplaza con `If-Match` y su
  ETag; si cambió, el guardado falla con un mensaje claro. Borrar siempre es enviar a la
  papelera.
- Las rutas con `..` se rechazan, no se siguen redirecciones, la verificación TLS no se puede
  desactivar y la contraseña de aplicación nunca aparece en logs ni en errores.
- El servidor **no autentica** a los clientes MCP: si usas HTTP, ponlo detrás de un proxy. En
  [`deploy/`](deploy) hay un ejemplo con nginx y un token Bearer.

Para empezar: copia `.env.example` a `.env`, completa `NEXTCLOUD_URL`, `NEXTCLOUD_USER` y
`NEXTCLOUD_APP_PASSWORD` (una contraseña de aplicación de Nextcloud) y ejecuta `make build`. La
configuración completa está en [docs/configuration.md](docs/configuration.md), las herramientas
en [docs/tools.md](docs/tools.md) y el modelo de seguridad en [docs/security.md](docs/security.md)
(estos documentos están en inglés).

## License

[MIT](LICENSE)
