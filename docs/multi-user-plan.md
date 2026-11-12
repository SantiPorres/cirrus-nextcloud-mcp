# Plan: multi-user mode (not implemented)

Today one server process serves one Nextcloud account, taken from the environment. This note
describes how a single deployment could serve many users, each with their own Nextcloud
credentials. **None of it is implemented**; it is here so the current design does not close the
door on it.

## Goal

Each MCP client sends its own Nextcloud credentials with every request. The server uses them for
that request only, and keeps nothing between requests: no shared state, no cookies, no sessions.

## Design

1. **Credentials per request.** In multi-user mode (`MCP_MULTI_USER=true`, HTTP transport only)
   the server reads the user id and app password from request headers, for example
   `X-Nextcloud-User` and `X-Nextcloud-App-Password`, or a single `Authorization: Basic …` meant
   for Nextcloud. `NEXTCLOUD_USER` and `NEXTCLOUD_APP_PASSWORD` must then be unset, so that a
   request without credentials can never fall back to a shared account.
2. **A client per request.** `mcp.NewStreamableHTTPHandler` already takes a
   `func(*http.Request) *mcp.Server`. That function would build a `nextcloud.Client` from the
   request's credentials and register the tools on a fresh server. Tool handlers already receive
   the client through the registry instead of a global, so they need no change.
3. **Nothing shared.** The HTTP transport is already stateless (`Stateless: true`). What must
   become per-request or be dropped:
   - the capabilities cache in `nextcloud.Client` (per client, so it dies with the request);
   - the pooled `http.Transport`: connections may be shared, but no cookie jar may ever be
     added, and the client must keep refusing redirects;
   - any future cache must be keyed by user or not exist.
4. **Server-wide policy stays server-wide.** Permissions (`NEXTCLOUD_ALLOW_*`), tool groups,
   editable extensions and limits remain operator settings from the environment. A user can do
   less than the policy allows (Nextcloud enforces their own rights), never more.
5. **Root per user.** `NEXTCLOUD_ROOT` would accept a template such as `/Shared/{user}` or stay a
   fixed folder that every user sees through their own permissions.
6. **stdio is unchanged.** A local process serves one user; multi-user applies to HTTP only.

## Security requirements before shipping it

- The proxy in front still authenticates the *caller*; the Nextcloud credentials authenticate the
  *account*. Both are required, and the credential headers must only travel over TLS.
- Credential headers must be removed from every log line and error, exactly as the app password
  is today (`Secret` type and scrubbing).
- Rate limiting per user, so one caller cannot exhaust another's Nextcloud brute-force budget.
- Tests proving isolation: two users on one server, interleaved requests, each seeing only their
  own files, trash, shares and activity; and a request without credentials failing closed.
- `get_server_info` must not reveal which users have connected.

## Out of scope

OAuth/OIDC against Nextcloud, token exchange, and storing credentials on the server. Those need
state, which is exactly what this mode avoids.
