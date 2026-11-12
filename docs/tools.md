# Tool reference

<!-- Generated from the server's own tool list. Do not edit by hand:
     run `UPDATE_DOCS=1 go test ./internal/server -run TestToolReference` instead. -->

Every tool the server can register, with all groups and permissions enabled, the folder prefix set to
`/Projects` and the spreadsheet pattern `*.xlsx`. A real server only exposes the tools its configuration allows;
`get_server_info` lists them and explains why the others are missing.

## `get_server_info`

Describes the server that is answering: version and commit, start time, enabled tool groups, the tools it exposes, the tools that are switched off and why, and the configuration flags. Use it to tell an old deployment from a client with an outdated tool list. It never returns credentials, the Nextcloud address or the account name. With check_connection=true it also reports whether Nextcloud is reachable and its version.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `check_connection` | boolean | no | Also contact Nextcloud and report whether it answers, its version and whether the trash bin and versions are enabled. Defaults to false. |

## `nc_activity_list`

Lists recent activity of the account (who changed, shared or deleted what), newest first. When the server is restricted to a folder, only file activity inside it is returned. Pass the smallest "activity_id" of a page as since_id to get older entries.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `limit` | integer | no | Maximum number of entries to fetch (1-200). Defaults to 30. |
| `since_id` | integer | no | Return entries older than this activity_id. Defaults to the newest. |

## `nc_comment_add`

Adds a comment to a file or folder, as the account this server uses. Comments are visible to everyone with access to the file.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `message` | string | yes | The comment text (at most 1000 characters). |
| `target` | string | yes | The file or folder: a path relative to the top folder, or an internal link (https://<host>/f/<id>). |

## `nc_comments_list`

Lists the comments people left on a file or folder, newest first.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `target` | string | yes | The file or folder: a path relative to the top folder, or an internal link (https://<host>/f/<id>). |
| `limit` | integer | no | Maximum number of comments (1-100). Defaults to 20. |
| `offset` | integer | no | Number of comments to skip. Defaults to 0. |

## `nc_copy`

Copies a file or folder. "destination" is the complete path of the copy (including its name). It never replaces anything: if the destination exists the call fails. The destination's parent folder must exist.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `destination` | string | yes | Complete new path relative to the top folder, including the name, e.g. "/Archive/2025 report.md". |
| `source` | string | yes | The file or folder: a path relative to the top folder, or an internal link (https://<host>/f/<id>). |

## `nc_create_file`

Creates a new text file (.csv, .dio, .drawio, .json, .md, .txt, .xml, .yaml, .yml). It never replaces an existing file: if the path is taken the call fails; use nc_edit_file or nc_edit_lines to change a file. The parent folder must exist. JSON and XML content must be well formed.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `content` | string | yes | The full text content of the file. |
| `path` | string | yes | Where to create the file, relative to the top folder, e.g. "/Specs/new-notes.md". |

## `nc_create_folder`

Creates a folder and any missing parents. Folders can only be created inside /Projects. Folders that already exist are left as they are.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `path` | string | yes | Folder to create, relative to the top folder, e.g. "/Projects/Meetings/2026-01-15 Review". |

## `nc_delete`

Deletes a file or folder by moving it to the Nextcloud trash bin, from where it can be restored with nc_trash_restore. Nothing is ever deleted permanently: the call is refused when the trash bin is not available. Returns the "trash_id" needed to restore it.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `target` | string | yes | A path relative to the top folder, or an internal link (https://<host>/f/<id>). |

## `nc_download`

Downloads a file of any type as an embedded resource (not as text). Small files come in one piece. For larger files pass offset and length to fetch a part: the result has "next_offset" for the following call, and "etag", which should be passed back so that every part comes from the same version of the file. To read documents as text use nc_read_file instead.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `target` | string | yes | A path relative to the top folder, or an internal file link (https://<host>/f/<id>). |
| `etag` | string | no | The "etag" returned by the previous part, so that the download fails if the file changed in between. |
| `length` | integer | no | Number of bytes to fetch. Leave out to download the whole file in one piece (small files only). |
| `offset` | integer | no | Byte position to start from. Defaults to 0. |

## `nc_edit_file`

Edits a text file (.csv, .dio, .drawio, .json, .md, .txt, .xml, .yaml, .yml) by replacing an exact piece of text. Read the file first and copy old_text exactly, including whitespace. The save is refused if the file changed in the meantime or if the result would be invalid JSON/XML. Compressed draw.io files are saved back uncompressed. For long files or whole blocks prefer nc_read_lines + nc_edit_lines.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `new_text` | string | yes | The replacement text. Use an empty string to delete old_text. |
| `old_text` | string | yes | The exact text to replace. Must appear exactly once unless replace_all is true. |
| `target` | string | yes | A path relative to the top folder (e.g. "/Specs/readme.md") or an internal file link (https://<host>/f/<id>). |
| `replace_all` | boolean | no | Replace every occurrence instead of requiring a single one. Defaults to false. |

## `nc_edit_lines`

Edits a text file (.csv, .dio, .drawio, .json, .md, .txt, .xml, .yaml, .yml) by line numbers. Call nc_read_lines first and pass its "file_hash"; the edit is refused if the file changed since. Lines are 1-based and inclusive. Replace 5-8: start_line=5, end_line=8. Delete 5-8: same with new_text="". Insert before 5: start_line=5, end_line=4. Append to a file of N lines: start_line=N+1, end_line=N. After an edit, use the returned "file_hash" and "total_lines" for the next one.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `end_line` | integer | yes | Last line to replace. Use start_line - 1 to insert without replacing. |
| `file_hash` | string | yes | The "file_hash" returned by nc_read_lines (or by the previous edit). |
| `start_line` | integer | yes | First line to replace, or the line before which to insert. |
| `target` | string | yes | A path relative to the top folder (e.g. "/Specs/readme.md") or an internal file link (https://<host>/f/<id>). |
| `new_text` | string | no | Replacement content, without line numbers. Empty deletes the lines. A final line break is optional. |

## `nc_favorite_set`

Marks a file or folder as favorite, or removes the mark with favorite=false.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `target` | string | yes | The file or folder: a path relative to the top folder, or an internal link (https://<host>/f/<id>). |
| `favorite` | boolean | no | true to mark as favorite, false to remove the mark. Defaults to true. |

## `nc_favorites_list`

Lists the files and folders marked as favorite.

*read-only*

No parameters.

## `nc_get_file_info`

Returns the metadata of a file or folder: id, ETag, size, dates, MIME type and what the account is permitted to do with it.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `target` | string | yes | A path relative to the top folder (e.g. "/Specs/readme.md") or an internal file link (https://<host>/f/<id>). |

## `nc_list_files`

Lists the files and folders directly inside a Nextcloud folder, folders first. Each item has a "path" and a "url" that the other tools accept.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `path` | string | no | Folder path relative to the top folder, e.g. "/" or "/Projects/Specs". Defaults to the top folder. |

## `nc_move`

Moves or renames a file or folder. "destination" is the complete new path (including the new name), not the folder to move into. It never replaces anything: if the destination exists the call fails. The destination's parent folder must exist.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `destination` | string | yes | Complete new path relative to the top folder, including the name, e.g. "/Archive/2025 report.md". |
| `source` | string | yes | The file or folder: a path relative to the top folder, or an internal link (https://<host>/f/<id>). |

## `nc_read_file`

Reads the text content of a file. Supports PDF, Word (.docx), Excel (.xlsx), draw.io diagrams and text files such as Markdown. Long files are paged: when the result has "next_offset", call again with offset=next_offset.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `target` | string | yes | A path relative to the top folder (e.g. "/Specs/readme.md") or an internal file link (https://<host>/f/<id>). |
| `max_chars` | integer | no | Maximum number of characters to return (1-100000). Defaults to 40000. |
| `offset` | integer | no | Character position to start reading from. Defaults to 0. |

## `nc_read_image`

Returns an image file as image content the model can look at. Large pictures are scaled down to max_side and photos are turned upright. Supports JPEG, PNG, GIF, WebP, BMP and TIFF; for other formats (such as HEIC or SVG) it falls back to the preview Nextcloud can generate.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `target` | string | yes | The image: a path relative to the top folder, or an internal file link (https://<host>/f/<id>). |
| `max_side` | integer | no | Largest width or height of the returned image, in pixels (64-4096). Defaults to 1568. |

## `nc_read_lines`

Reads a text file with line numbers, to prepare nc_edit_lines. Each returned line looks like "    12| text": the number and the "|" are NOT part of the file. Paged: when the result has "next_start_line", call again with start_line set to it. The result includes "file_hash", which nc_edit_lines requires.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `target` | string | yes | A path relative to the top folder (e.g. "/Specs/readme.md") or an internal file link (https://<host>/f/<id>). |
| `max_lines` | integer | no | Maximum number of lines to return (1-1000). Output is also cut at about 40000 characters. Defaults to 200. |
| `start_line` | integer | no | First line to return (1-based). Defaults to 1. |

## `nc_recent_files`

Lists the files modified in the last days, newest first.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `days` | integer | no | How many days back to look (1-365). Defaults to 7. |
| `limit` | integer | no | Maximum number of files (1-100). Defaults to 25. |

## `nc_render_pdf`

Renders pages of a PDF as images, for scanned documents, diagrams or layouts that nc_read_file cannot convey as text. At most 5 pages per call.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `target` | string | yes | The PDF: a path relative to the top folder, or an internal file link (https://<host>/f/<id>). |
| `dpi` | integer | no | Resolution in dots per inch (50-200). Defaults to 100. |
| `max_side` | integer | no | Largest width or height of each returned image, in pixels (64-4096). Defaults to 1568. |
| `pages` | string | no | Pages to render: "1", "2-4" or "1,3,5" (at most 5). Defaults to "1". |

## `nc_resolve_link`

Resolves an internal Nextcloud link (https://<host>/f/<id>) to the path and metadata of the file or folder it points to. Public share links (/s/<token>) are not supported.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `url` | string | yes | An internal Nextcloud link, https://<host>/f/<id>. |

## `nc_search_files`

Searches files and folders by name (the name must contain the query, case-insensitive). It does not search inside file contents.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `query` | string | yes | Text to look for in file and folder names. |
| `limit` | integer | no | Maximum number of results (1-50). Defaults to 25. |

## `nc_share_create`

Shares a file or folder, either as a public link (type "link") or with a Nextcloud user (type "user"). For public links: a password of at least 10 characters is required; an expiry date is required; the expiry date can be at most 30 days ahead. Shares are read-only unless permissions="edit"; the right to re-share is never granted. Tell the person the password through a different channel than the link.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `target` | string | yes | The file or folder to share: a path relative to the top folder, or an internal link (https://<host>/f/<id>). |
| `type` | string | yes | "link" for a public link, or "user" to share with a Nextcloud user. |
| `expires` | string | no | Last day the share works, as YYYY-MM-DD. |
| `label` | string | no | Optional note shown next to a public link in Nextcloud. |
| `password` | string | no | For type "link": the password people must enter to open the link. |
| `permissions` | string | no | "read" (default) or "edit". |
| `share_with` | string | no | For type "user": the user id to share with. |

## `nc_share_delete`

Stops sharing: the link stops working or the user loses access. The file itself is not touched.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `share_id` | string | yes | The "share_id" returned by nc_shares_list or nc_share_create. |

## `nc_shares_list`

Lists the shares created by this account: public links and shares with users. With "target", only the shares of that file or folder. Passwords are never returned.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `target` | string | no | Optional file or folder: a path relative to the top folder, or an internal link (https://<host>/f/<id>). |

## `nc_sheet_append_rows`

Adds records at the end of a table, copying the format of the last record. Each row is {header: value}; headers are matched ignoring case, and if any header does not exist nothing is written. If the sheet has an id column and a row has no id, the next free one is assigned (see "ids_assigned"); a duplicate id is refused. Read the sheet with nc_sheet_read first. Only spreadsheets matching these patterns can be modified: *.xlsx. The file is edited in place, saved only if nobody changed it in the meantime, and read again after saving ("verified"). Text starting with "=" is stored as text, never as a formula.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `rows` | array | yes | The records to add, each an object {header: value}. Values are text, numbers or booleans. |
| `target` | string | yes | Path of the .xlsx file relative to the top folder, or an internal file link (https://<host>/f/<id>). |
| `id_column` | string | no | Header of the identifier column. Defaults to "ID". |
| `sheet` | string | no | Sheet name. Defaults to the first sheet. |

## `nc_sheet_create`

Creates a new sheet with a header row. If the .xlsx file does not exist, a new workbook with that sheet is created (the parent folder must exist); if it exists, the sheet is added to it. An existing sheet is never replaced. Only spreadsheets matching these patterns can be modified: *.xlsx. The file is edited in place, saved only if nobody changed it in the meantime, and read again after saving ("verified"). Text starting with "=" is stored as text, never as a formula.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `headers` | array | yes | Column names for row 1. |
| `sheet` | string | yes | Name of the new sheet (at most 31 characters). |
| `target` | string | yes | Path of the .xlsx file relative to the top folder. It is created if it does not exist. |

## `nc_sheet_read`

Reads a sheet of an .xlsx file as a table: row 1 is the header and every other row becomes {header: value}. Returns the headers, the sheet names and, when the sheet has an id column, "next_id" (the next free id). Formulas are returned as their last calculated value and dates as ISO text. Paged with offset/next_offset.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `target` | string | yes | Path of the .xlsx file relative to the top folder, or an internal file link (https://<host>/f/<id>). |
| `id_column` | string | no | Header of the identifier column, used for next_id. Defaults to "ID". |
| `max_rows` | integer | no | Maximum number of records to return (1-2000). Defaults to 200. |
| `offset` | integer | no | Number of records to skip. Defaults to 0. |
| `sheet` | string | no | Sheet name. Defaults to the first sheet. |

## `nc_sheet_update_cells`

Changes cells of one existing record, found by the value of a key column (normally the id). Exactly one row must match and the key column cannot be changed. Pass "expected" (current values of some columns of that row, as read with nc_sheet_read) to make the change only if nobody edited them since; otherwise it is refused and nothing is written. Only spreadsheets matching these patterns can be modified: *.xlsx. The file is edited in place, saved only if nobody changed it in the meantime, and read again after saving ("verified"). Text starting with "=" is stored as text, never as a formula.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `changes` | object | yes | {header: new value}. An empty string clears the cell. |
| `key_column` | string | yes | Header that identifies the record, e.g. "ID". |
| `key_value` | any | yes | Value of the key column in the record to change, e.g. "D-12" or 7. |
| `target` | string | yes | Path of the .xlsx file relative to the top folder, or an internal file link (https://<host>/f/<id>). |
| `expected` | object | no | Optional {header: current value} that must still hold for the change to be made. |
| `sheet` | string | no | Sheet name. Defaults to the first sheet. |

## `nc_tag_assign`

Assigns an existing system tag (by name or id) to a file or folder. Assigning a tag twice has no further effect.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `tag` | string | yes | Tag name (case-insensitive) or id. The tag must already exist. |
| `target` | string | yes | The file or folder: a path relative to the top folder, or an internal link (https://<host>/f/<id>). |

## `nc_tag_create`

Creates a new system tag, visible to and assignable by users. Tags are shared by the whole Nextcloud instance.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Name of the new tag. |

## `nc_tag_remove`

Removes a system tag from a file or folder. The tag itself and the file are not deleted.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `tag` | string | yes | Tag name (case-insensitive) or id. The tag must already exist. |
| `target` | string | yes | The file or folder: a path relative to the top folder, or an internal link (https://<host>/f/<id>). |

## `nc_tag_search`

Lists the files and folders that carry a system tag, given by name or id.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `tag` | string | yes | Tag name (case-insensitive) or id. |
| `limit` | integer | no | Maximum number of results (1-500). Defaults to 50. |

## `nc_tags_list`

Lists the system tags of this Nextcloud. With "target", lists only the tags assigned to that file or folder.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `target` | string | no | Optional file or folder whose tags to list: a path relative to the top folder, or an internal link (https://<host>/f/<id>). |

## `nc_trash_list`

Lists deleted files and folders that can still be restored, newest first, with the "trash_id" that nc_trash_restore needs. The trash cannot be emptied from here.

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `limit` | integer | no | Maximum number of items (1-500). Defaults to 50. |

## `nc_trash_restore`

Restores a deleted file or folder to its original location. If something else now exists there, Nextcloud keeps both by giving the restored item a different name.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `trash_id` | string | yes | The "trash_id" returned by nc_trash_list or nc_delete. |

## `nc_upload_chunk`

Sends one piece of an upload, base64-encoded. Pieces are numbered from 1 and joined in that order; sending the same index again replaces that piece.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `data_base64` | string | yes | The bytes of this piece, base64-encoded. |
| `index` | integer | yes | Number of this piece, starting at 1. |
| `path` | string | yes | The same path given to nc_upload_start. |
| `upload_id` | string | yes | The "upload_id" returned by nc_upload_start. |

## `nc_upload_finish`

Joins the pieces of an upload into the new file. It fails, writing nothing, if a file appeared at that path in the meantime or if a piece is missing. Pass "sha256" to have the stored file checked against it.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `path` | string | yes | The same path given to nc_upload_start. |
| `upload_id` | string | yes | The "upload_id" returned by nc_upload_start. |
| `sha256` | string | no | Optional SHA-256 of the whole file (hex). The stored file is checked against it. |

## `nc_upload_start`

Starts the upload of a new file of any type and returns an "upload_id". Then send the content with nc_upload_chunk and create the file with nc_upload_finish. Uploads only create: if the path is taken the upload is refused, here and again when finishing. The parent folder must exist.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `path` | string | yes | Where to create the file, relative to the top folder, e.g. "/Scans/invoice.pdf". |
| `total_size` | integer | no | Size of the whole file in bytes, if known. Used to refuse files that are too large before sending them. |

## `nc_user_info`

Returns the Nextcloud account this server is connected as (id, display name, e-mail, groups) and its storage quota.

*read-only*

No parameters.

## `nc_versions_list`

Lists the previous versions Nextcloud keeps of a file, newest first, with the "version_id" that nc_versions_restore needs and the file's "current_etag".

*read-only*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `target` | string | yes | A path relative to the top folder, or an internal link (https://<host>/f/<id>). |

## `nc_versions_restore`

Makes a previous version the current content of a file. Pass the "current_etag" returned by nc_versions_list: the restore is refused if the file changed since. Nextcloud keeps the replaced content as another version, so the restore can itself be undone.

*changes data*

| Parameter | Type | Required | Description |
|---|---|---|---|
| `etag` | string | yes | The "current_etag" returned by nc_versions_list, proving the file has not changed since. |
| `target` | string | yes | The file: a path relative to the top folder, or an internal link (https://<host>/f/<id>). |
| `version_id` | string | yes | The "version_id" returned by nc_versions_list. |
