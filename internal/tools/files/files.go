// Package files implements the "files" tool group: browsing, reading and
// editing text documents.
package files

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/config"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/extract"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/nextcloud"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/textedit"
	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/tools"
)

const (
	defaultMaxChars   = 40000
	maxPageChars      = 100000
	defaultMaxLines   = 200
	maxLinesPerRead   = 1000
	maxLinesReadChars = 40000
	defaultSearch     = 25
	maxSearchResults  = 50
	textContentType   = "text/plain; charset=utf-8"
)

type group struct {
	client *nextcloud.Client
	cfg    *config.Config
}

// Register adds the file tools to the registry.
func Register(r *tools.Registry) {
	g := &group{client: r.Client, cfg: r.Config}
	extensions := strings.Join(r.Config.SortedExtensions(), ", ")

	tools.Add(r, tools.Spec{
		Name: "nc_list_files", Title: "List files", Group: config.GroupFiles,
		Description: "Lists the files and folders directly inside a Nextcloud folder, folders first. " +
			"Each item has a \"path\" and a \"url\" that the other tools accept.",
	}, g.list)

	tools.Add(r, tools.Spec{
		Name: "nc_search_files", Title: "Search files by name", Group: config.GroupFiles,
		Description: "Searches files and folders by name (the name must contain the query, case-insensitive). " +
			"It does not search inside file contents.",
	}, g.search)

	tools.Add(r, tools.Spec{
		Name: "nc_get_file_info", Title: "File information", Group: config.GroupFiles,
		Description: "Returns the metadata of a file or folder: id, ETag, size, dates, MIME type and what the " +
			"account is permitted to do with it.",
	}, g.info)

	tools.Add(r, tools.Spec{
		Name: "nc_read_file", Title: "Read file", Group: config.GroupFiles,
		Description: "Reads the text content of a file. Supports PDF, Word (.docx), Excel (.xlsx), draw.io diagrams " +
			"and text files such as Markdown. Long files are paged: when the result has \"next_offset\", call again " +
			"with offset=next_offset.",
	}, g.read)

	tools.Add(r, tools.Spec{
		Name: "nc_read_lines", Title: "Read file lines", Group: config.GroupFiles,
		Description: "Reads a text file with line numbers, to prepare nc_edit_lines. Each returned line looks like " +
			"\"    12| text\": the number and the \"|\" are NOT part of the file. Paged: when the result has " +
			"\"next_start_line\", call again with start_line set to it. The result includes \"file_hash\", which " +
			"nc_edit_lines requires.",
	}, g.readLines)

	tools.Add(r, tools.Spec{
		Name: "nc_resolve_link", Title: "Resolve file link", Group: config.GroupFiles,
		Description: "Resolves an internal Nextcloud link (https://<host>/f/<id>) to the path and metadata of the file " +
			"or folder it points to. Public share links (/s/<token>) are not supported.",
	}, g.resolveLink)

	tools.Add(r, tools.Spec{
		Name: "nc_create_file", Title: "Create text file", Group: config.GroupFiles, Permission: tools.Write,
		Description: "Creates a new text file (" + extensions + "). It never replaces an existing file: if the path " +
			"is taken the call fails; use nc_edit_file or nc_edit_lines to change a file. The parent folder must " +
			"exist. JSON and XML content must be well formed.",
	}, g.create)

	tools.Add(r, tools.Spec{
		Name: "nc_edit_file", Title: "Edit file by replacement", Group: config.GroupFiles, Permission: tools.Write, Destructive: true,
		Description: "Edits a text file (" + extensions + ") by replacing an exact piece of text. Read the file first " +
			"and copy old_text exactly, including whitespace. The save is refused if the file changed in the " +
			"meantime or if the result would be invalid JSON/XML. Compressed draw.io files are saved back " +
			"uncompressed. For long files or whole blocks prefer nc_read_lines + nc_edit_lines.",
	}, g.edit)

	tools.Add(r, tools.Spec{
		Name: "nc_edit_lines", Title: "Edit file by line range", Group: config.GroupFiles, Permission: tools.Write, Destructive: true,
		Description: "Edits a text file (" + extensions + ") by line numbers. Call nc_read_lines first and pass its " +
			"\"file_hash\"; the edit is refused if the file changed since. Lines are 1-based and inclusive. " +
			"Replace 5-8: start_line=5, end_line=8. Delete 5-8: same with new_text=\"\". Insert before 5: " +
			"start_line=5, end_line=4. Append to a file of N lines: start_line=N+1, end_line=N. After an edit, " +
			"use the returned \"file_hash\" and \"total_lines\" for the next one.",
	}, g.editLines)

	folderSpec := tools.Spec{
		Name: "nc_create_folder", Title: "Create folder", Group: config.GroupFiles, Permission: tools.Write, Idempotent: true,
		Description: "Creates a folder and any missing parents. Folders can only be created inside " +
			displayFolder(r.Config.FolderWritePrefix) + ". Folders that already exist are left as they are.",
	}
	if r.Config.FolderWritePrefix == "" {
		folderSpec.Unavailable = "NEXTCLOUD_FOLDER_WRITE_PREFIX is not set"
	}
	tools.Add(r, folderSpec, g.createFolder)
}

func displayFolder(folder string) string {
	if folder == "" {
		return "/"
	}
	return folder
}

// --- listing and metadata ---------------------------------------------------

type listInput struct {
	Path string `json:"path,omitempty" jsonschema:"Folder path relative to the top folder, e.g. \"/\" or \"/Projects/Specs\". Defaults to the top folder."`
}

func (g *group) list(ctx context.Context, in listInput) (any, error) {
	folder, err := g.client.Resolve(ctx, in.Path, false)
	if err != nil {
		return nil, err
	}
	entries, err := g.client.List(ctx, folder)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": folder.Rel, "count": len(entries), "items": tools.EntryMaps(g.client, entries)}, nil
}

type searchInput struct {
	Query string `json:"query" jsonschema:"Text to look for in file and folder names."`
	Limit int    `json:"limit,omitempty" jsonschema:"Maximum number of results (1-50). Defaults to 25."`
}

func (g *group) search(ctx context.Context, in searchInput) (any, error) {
	if strings.TrimSpace(in.Query) == "" {
		return nil, nextcloud.Errorf("query must not be empty.")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultSearch
	}
	entries, err := g.client.SearchByName(ctx, in.Query, min(limit, maxSearchResults))
	if err != nil {
		return nil, err
	}
	return map[string]any{"query": in.Query, "count": len(entries), "results": tools.EntryMaps(g.client, entries)}, nil
}

type targetInput struct {
	Target string `json:"target" jsonschema:"A path relative to the top folder (e.g. \"/Specs/readme.md\") or an internal file link (https://<host>/f/<id>)."`
}

func (g *group) infoMap(e nextcloud.Entry) map[string]any {
	out := tools.EntryMap(g.client, e)
	out["file_id"] = e.FileID
	out["etag"] = e.ETag
	out["favorite"] = e.Favorite
	if e.Created > 0 {
		out["created"] = time.Unix(e.Created, 0).UTC().Format(time.RFC3339)
	}
	if e.Uploaded > 0 {
		out["uploaded"] = time.Unix(e.Uploaded, 0).UTC().Format(time.RFC3339)
	}
	has := func(letter string) bool { return strings.Contains(e.Permissions, letter) }
	out["permissions"] = map[string]any{
		"raw":        e.Permissions,
		"can_read":   has("G"),
		"can_write":  has("W") || (e.IsFolder && has("C") && has("K")),
		"can_create": e.IsFolder && has("C") && has("K"),
		"can_delete": has("D"),
		"can_rename": has("N"),
		"can_move":   has("V"),
		"can_share":  has("R"),
		"is_shared":  has("S"),
		"is_mounted": has("M"),
	}
	return out
}

func (g *group) info(ctx context.Context, in targetInput) (any, error) {
	target, err := g.client.Resolve(ctx, in.Target, false)
	if err != nil {
		return nil, err
	}
	entry, err := g.client.Stat(ctx, target)
	if err != nil {
		return nil, err
	}
	return g.infoMap(entry), nil
}

type linkInput struct {
	URL string `json:"url" jsonschema:"An internal Nextcloud link, https://<host>/f/<id>."`
}

func (g *group) resolveLink(ctx context.Context, in linkInput) (any, error) {
	fileID, err := g.client.ParseFileLink(in.URL)
	if err != nil {
		return nil, err
	}
	entry, err := g.client.FindByID(ctx, fileID)
	if err != nil {
		return nil, err
	}
	return g.infoMap(entry), nil
}

// --- reading ----------------------------------------------------------------

// fetchText downloads a file and extracts its text.
func (g *group) fetchText(ctx context.Context, rawTarget string) (nextcloud.Target, string, int, error) {
	target, err := g.client.Resolve(ctx, rawTarget, true)
	if err != nil {
		return target, "", 0, err
	}
	data, _, err := g.client.Download(ctx, target, g.cfg.MaxDownloadBytes)
	if err != nil {
		return target, "", 0, err
	}
	text, err := extract.Text(target.Name, data)
	return target, text, len(data), err
}

type readInput struct {
	Target   string `json:"target" jsonschema:"A path relative to the top folder (e.g. \"/Specs/readme.md\") or an internal file link (https://<host>/f/<id>)."`
	Offset   int    `json:"offset,omitempty" jsonschema:"Character position to start reading from. Defaults to 0."`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"Maximum number of characters to return (1-100000). Defaults to 40000."`
}

func (g *group) read(ctx context.Context, in readInput) (any, error) {
	if in.Offset < 0 {
		return nil, nextcloud.Errorf("offset must not be negative.")
	}
	maxChars := in.MaxChars
	if maxChars <= 0 {
		maxChars = defaultMaxChars
	}
	maxChars = min(maxChars, maxPageChars)

	target, text, size, err := g.fetchText(ctx, in.Target)
	if err != nil {
		return nil, err
	}
	runes := []rune(strings.ReplaceAll(text, "\r\n", "\n"))
	total := len(runes)
	if in.Offset > total {
		return nil, nextcloud.Errorf("offset %d is past the end of the file (%d characters).", in.Offset, total)
	}
	end := min(in.Offset+maxChars, total)
	out := map[string]any{
		"path":           target.Rel,
		"filename":       target.Name,
		"size_bytes":     size,
		"total_chars":    total,
		"offset":         in.Offset,
		"returned_chars": end - in.Offset,
		"truncated":      end < total,
		"content":        string(runes[in.Offset:end]),
	}
	if end < total {
		out["next_offset"] = end
	}
	return out, nil
}

type readLinesInput struct {
	Target    string `json:"target" jsonschema:"A path relative to the top folder (e.g. \"/Specs/readme.md\") or an internal file link (https://<host>/f/<id>)."`
	StartLine int    `json:"start_line,omitempty" jsonschema:"First line to return (1-based). Defaults to 1."`
	MaxLines  int    `json:"max_lines,omitempty" jsonschema:"Maximum number of lines to return (1-1000). Output is also cut at about 40000 characters. Defaults to 200."`
}

func (g *group) readLines(ctx context.Context, in readLinesInput) (any, error) {
	start := in.StartLine
	if start == 0 {
		start = 1
	}
	if start < 1 {
		return nil, nextcloud.Errorf("start_line must be 1 or greater.")
	}
	maxLines := in.MaxLines
	if maxLines <= 0 {
		maxLines = defaultMaxLines
	}
	maxLines = min(maxLines, maxLinesPerRead)

	target, text, _, err := g.fetchText(ctx, in.Target)
	if err != nil {
		return nil, err
	}
	lines, _ := textedit.SplitLines(strings.ReplaceAll(text, "\r\n", "\n"))
	total := len(lines)
	if total > 0 && start > total {
		return nil, nextcloud.Errorf("start_line %d is past the end of the file (%d lines).", start, total)
	}

	var chosen []string
	chars := 0
	for i := start - 1; i < total && len(chosen) < maxLines; i++ {
		length := utf8.RuneCountInString(lines[i])
		if len(chosen) > 0 && chars+length > maxLinesReadChars {
			break
		}
		chosen = append(chosen, lines[i])
		chars += length + 8
	}
	last := start - 1 + len(chosen)
	out := map[string]any{
		"path":        target.Rel,
		"filename":    target.Name,
		"file_hash":   textedit.Hash(text),
		"total_lines": total,
		"start_line":  start,
		"end_line":    last,
		"content":     textedit.Numbered(chosen, start),
	}
	if last < total {
		out["next_start_line"] = last + 1
	}
	return out, nil
}

// --- writing ----------------------------------------------------------------

func (g *group) checkExtension(name string) error {
	if g.cfg.ExtensionEditable(name) {
		return nil
	}
	return nextcloud.Errorf("Files named like %q cannot be created or modified. Allowed extensions: %s.",
		name, strings.Join(g.cfg.SortedExtensions(), ", "))
}

func (g *group) checkSize(text string) error {
	if int64(len(text)) > g.cfg.MaxTextBytes {
		return nextcloud.Errorf("The content is too large (%d bytes, limit %d).", len(text), g.cfg.MaxTextBytes)
	}
	return nil
}

type createInput struct {
	Path    string `json:"path" jsonschema:"Where to create the file, relative to the top folder, e.g. \"/Specs/new-notes.md\"."`
	Content string `json:"content" jsonschema:"The full text content of the file."`
}

func (g *group) create(ctx context.Context, in createInput) (any, error) {
	target, err := g.client.PathTarget(in.Path, true)
	if err != nil {
		return nil, err
	}
	if err := g.checkExtension(target.Name); err != nil {
		return nil, err
	}
	if err := g.checkSize(in.Content); err != nil {
		return nil, err
	}
	if err := textedit.Validate(target.Name, in.Content); err != nil {
		return nil, err
	}
	etag, err := g.client.Put(ctx, target, []byte(in.Content), textContentType, nextcloud.PutCondition{CreateOnly: true})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path":       target.Rel,
		"status":     "created",
		"size_chars": utf8.RuneCountInString(in.Content),
		"etag":       etag,
	}, nil
}

// editText is the shared flow of the editing tools: download with the ETag,
// transform in memory, validate, and save with If-Match so a concurrent
// change makes the save fail instead of being overwritten.
//
// transform receives the text with "\n" line endings when the file uses
// "\r\n" uniformly; the original style is restored on save.
func (g *group) editText(ctx context.Context, rawTarget string, transform func(text string) (string, error)) (nextcloud.Target, string, error) {
	target, err := g.client.Resolve(ctx, rawTarget, true)
	if err != nil {
		return target, "", err
	}
	if err := g.checkExtension(target.Name); err != nil {
		return target, "", err
	}
	data, etag, err := g.client.Download(ctx, target, g.cfg.MaxTextBytes)
	if err != nil {
		return target, "", err
	}
	if !utf8.Valid(data) {
		return target, "", nextcloud.Errorf("The file is not valid UTF-8 text.")
	}
	text := string(data)
	if extract.IsDrawio(target.Name) {
		text = extract.DecodeDrawio(text)
	}
	crlf := textedit.UniformCRLF(text)
	if crlf {
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}

	updated, err := transform(text)
	if err != nil {
		return target, "", err
	}
	if err := textedit.Validate(target.Name, updated); err != nil {
		return target, "", err
	}
	if crlf {
		updated = strings.ReplaceAll(updated, "\n", "\r\n")
	}
	if err := g.checkSize(updated); err != nil {
		return target, "", err
	}
	if _, err := g.client.Put(ctx, target, []byte(updated), textContentType, nextcloud.PutCondition{IfMatch: etag}); err != nil {
		return target, "", err
	}
	return target, updated, nil
}

type editInput struct {
	Target     string `json:"target" jsonschema:"A path relative to the top folder (e.g. \"/Specs/readme.md\") or an internal file link (https://<host>/f/<id>)."`
	OldText    string `json:"old_text" jsonschema:"The exact text to replace. Must appear exactly once unless replace_all is true."`
	NewText    string `json:"new_text" jsonschema:"The replacement text. Use an empty string to delete old_text."`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"Replace every occurrence instead of requiring a single one. Defaults to false."`
}

func (g *group) edit(ctx context.Context, in editInput) (any, error) {
	if in.OldText == "" {
		return nil, nextcloud.Errorf("old_text must not be empty.")
	}
	if in.OldText == in.NewText {
		return nil, nextcloud.Errorf("old_text and new_text are identical; nothing to change.")
	}
	oldText := strings.ReplaceAll(in.OldText, "\r\n", "\n")
	newText := strings.ReplaceAll(in.NewText, "\r\n", "\n")

	var result textedit.ReplaceResult
	target, saved, err := g.editText(ctx, in.Target, func(text string) (string, error) {
		var err error
		result, err = textedit.Replace(text, oldText, newText, in.ReplaceAll)
		return result.Text, err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path":         target.Rel,
		"filename":     target.Name,
		"replacements": result.Replacements,
		"matched":      result.Matched,
		"size_chars":   utf8.RuneCountInString(saved),
		"file_hash":    textedit.Hash(saved),
	}, nil
}

type editLinesInput struct {
	Target    string `json:"target" jsonschema:"A path relative to the top folder (e.g. \"/Specs/readme.md\") or an internal file link (https://<host>/f/<id>)."`
	FileHash  string `json:"file_hash" jsonschema:"The \"file_hash\" returned by nc_read_lines (or by the previous edit)."`
	StartLine int    `json:"start_line" jsonschema:"First line to replace, or the line before which to insert."`
	EndLine   int    `json:"end_line" jsonschema:"Last line to replace. Use start_line - 1 to insert without replacing."`
	NewText   string `json:"new_text,omitempty" jsonschema:"Replacement content, without line numbers. Empty deletes the lines. A final line break is optional."`
}

func (g *group) editLines(ctx context.Context, in editLinesInput) (any, error) {
	var result textedit.LineEdit
	target, saved, err := g.editText(ctx, in.Target, func(text string) (string, error) {
		if textedit.Hash(text) != in.FileHash {
			return "", nextcloud.Errorf("The file has changed since it was read, or file_hash is wrong. Call nc_read_lines again and retry.")
		}
		var err error
		result, err = textedit.ReplaceLines(text, in.StartLine, in.EndLine, in.NewText)
		return result.Text, err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path":           target.Rel,
		"filename":       target.Name,
		"lines_replaced": result.LinesReplaced,
		"lines_inserted": result.LinesInserted,
		"total_lines":    result.TotalLines,
		"file_hash":      textedit.Hash(saved),
		"preview":        result.Preview,
	}, nil
}

type folderInput struct {
	Path string `json:"path" jsonschema:"Folder to create, relative to the top folder, e.g. \"/Projects/Meetings/2026-01-15 Review\"."`
}

func (g *group) createFolder(ctx context.Context, in folderInput) (any, error) {
	target, err := g.client.PathTarget(in.Path, false)
	if err != nil {
		return nil, err
	}
	prefix := g.cfg.FolderWritePrefix
	if !strings.HasPrefix(target.Rel, prefix+"/") {
		return nil, nextcloud.Errorf("Folders can only be created inside %s.", prefix)
	}

	// Create every missing level below the prefix, which itself must exist.
	created, existing := []string{}, []string{}
	current := prefix
	for _, segment := range strings.Split(strings.TrimPrefix(target.Rel, prefix+"/"), "/") {
		current += "/" + segment
		step, err := g.client.PathTarget(current, false)
		if err != nil {
			return nil, err
		}
		isNew, err := g.client.Mkcol(ctx, step)
		if err != nil {
			return nil, err
		}
		if isNew {
			created = append(created, current)
		} else {
			existing = append(existing, current)
		}
	}
	return map[string]any{"path": target.Rel, "created": created, "already_existed": existing}, nil
}
