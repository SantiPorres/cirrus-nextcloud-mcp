// Package config loads the server configuration from environment variables.
//
// Everything is read once at startup. Nothing here is specific to one
// Nextcloud instance: folders, extensions and spreadsheet patterns are all
// supplied by the operator.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Tool groups that can be switched on or off with NEXTCLOUD_TOOL_GROUPS.
const (
	GroupFiles       = "files"
	GroupSheets      = "sheets"
	GroupSafeOps     = "safe_ops"
	GroupBinary      = "binary"
	GroupMetadata    = "metadata"
	GroupSharing     = "sharing"
	GroupDiagnostics = "diagnostics"
)

// AllGroups lists every known group, in display order.
var AllGroups = []string{
	GroupFiles, GroupSheets, GroupSafeOps, GroupBinary, GroupMetadata, GroupSharing, GroupDiagnostics,
}

// DefaultGroups are enabled when NEXTCLOUD_TOOL_GROUPS is not set. Sharing is
// deliberately left out: it must be requested explicitly.
var DefaultGroups = []string{
	GroupFiles, GroupSheets, GroupSafeOps, GroupBinary, GroupMetadata, GroupDiagnostics,
}

// DefaultEditableExtensions is used when NEXTCLOUD_EDITABLE_EXTENSIONS is not set.
var DefaultEditableExtensions = []string{
	".md", ".txt", ".json", ".csv", ".yml", ".yaml", ".xml", ".drawio", ".dio",
}

// Transports accepted in MCP_TRANSPORT.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "streamable-http"
)

// Secret is a string that never prints its value.
type Secret string

// String implements fmt.Stringer and hides the value.
func (Secret) String() string { return "[redacted]" }

// GoString hides the value from %#v as well.
func (Secret) GoString() string { return "[redacted]" }

// MarshalText hides the value from JSON and slog.
func (Secret) MarshalText() ([]byte, error) { return []byte("[redacted]"), nil }

// Reveal returns the actual secret. Call it only where the value is sent to Nextcloud.
func (s Secret) Reveal() string { return string(s) }

// Config is the validated server configuration.
type Config struct {
	// Connection.
	BaseURL     *url.URL
	User        string
	AppPassword Secret

	// Permissions. All false by default: the server is read-only.
	AllowWrite  bool
	AllowDelete bool
	AllowShare  bool

	// Root is the only folder the tools can see, as "/a/b", or "" for the whole account.
	Root string
	// FolderWritePrefix is the folder (relative to Root, as "/a/b") under which
	// folders may be created. "" disables folder creation.
	FolderWritePrefix string
	// EditableExtensions are the lowercase extensions (with dot) of text files that may be created or edited.
	EditableExtensions []string
	// EditableSpreadsheets are lowercase glob patterns matched against the file
	// name or the path relative to Root. Empty means no spreadsheet can be modified.
	EditableSpreadsheets []string
	// ProtectedSheets are lowercase sheet names that are never written to.
	ProtectedSheets []string

	// Groups holds the enabled tool groups.
	Groups map[string]bool

	// Sharing policy.
	ShareRequirePassword bool
	ShareRequireExpiry   bool
	ShareMaxExpiryDays   int

	// Server.
	Transport string
	HTTPAddr  string

	// Limits, in bytes.
	MaxDownloadBytes int64
	MaxTextBytes     int64
}

// Getenv is the signature of os.Getenv, so tests can supply their own environment.
type Getenv func(string) string

// Load reads and validates the configuration. The returned error never contains secret values.
func Load(getenv Getenv) (*Config, error) {
	var problems []string
	fail := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	get := func(name string) string { return strings.TrimSpace(getenv(name)) }
	flag := func(name string, fallback bool) bool {
		value := strings.ToLower(get(name))
		switch value {
		case "":
			return fallback
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
		fail("%s must be true or false", name)
		return fallback
	}
	number := func(name string, fallback int64, minimum int64) int64 {
		value := get(name)
		if value == "" {
			return fallback
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < minimum {
			fail("%s must be an integer of at least %d", name, minimum)
			return fallback
		}
		return n
	}

	cfg := &Config{
		User:        get("NEXTCLOUD_USER"),
		AppPassword: Secret(getenv("NEXTCLOUD_APP_PASSWORD")),
		AllowWrite:  flag("NEXTCLOUD_ALLOW_WRITE", false),
		AllowDelete: flag("NEXTCLOUD_ALLOW_DELETE", false),
		AllowShare:  flag("NEXTCLOUD_ALLOW_SHARE", false),

		ShareRequirePassword: flag("NEXTCLOUD_SHARE_REQUIRE_PASSWORD", true),
		ShareRequireExpiry:   flag("NEXTCLOUD_SHARE_REQUIRE_EXPIRY", true),
		ShareMaxExpiryDays:   int(number("NEXTCLOUD_SHARE_MAX_EXPIRY_DAYS", 30, 1)),

		Transport: strings.ToLower(get("MCP_TRANSPORT")),
		HTTPAddr:  get("MCP_HTTP_ADDR"),

		MaxDownloadBytes: number("NEXTCLOUD_MAX_DOWNLOAD_BYTES", 25*1024*1024, 1),
		MaxTextBytes:     number("NEXTCLOUD_MAX_TEXT_BYTES", 2*1024*1024, 1),
	}

	// Connection.
	rawURL := strings.TrimRight(get("NEXTCLOUD_URL"), "/")
	if rawURL == "" || cfg.User == "" || cfg.AppPassword == "" {
		fail("NEXTCLOUD_URL, NEXTCLOUD_USER and NEXTCLOUD_APP_PASSWORD are required")
	} else if parsed, err := url.Parse(rawURL); err != nil || parsed.Host == "" {
		fail("NEXTCLOUD_URL is not a valid URL")
	} else {
		switch {
		case parsed.User != nil:
			fail("NEXTCLOUD_URL must not contain credentials; use NEXTCLOUD_USER and NEXTCLOUD_APP_PASSWORD")
		case parsed.RawQuery != "" || parsed.Fragment != "":
			fail("NEXTCLOUD_URL must not contain a query string or a fragment")
		case parsed.Scheme == "https":
		case parsed.Scheme == "http":
			if !flag("NEXTCLOUD_ALLOW_INSECURE_HTTP", false) {
				fail("NEXTCLOUD_URL must use https (set NEXTCLOUD_ALLOW_INSECURE_HTTP=true only for local testing)")
			}
		default:
			fail("NEXTCLOUD_URL must start with https://")
		}
		cfg.BaseURL = parsed
	}

	// Scope.
	var err error
	if cfg.Root, err = cleanFolder(get("NEXTCLOUD_ROOT")); err != nil {
		fail("NEXTCLOUD_ROOT %v", err)
	}
	if cfg.FolderWritePrefix, err = cleanFolder(get("NEXTCLOUD_FOLDER_WRITE_PREFIX")); err != nil {
		fail("NEXTCLOUD_FOLDER_WRITE_PREFIX %v", err)
	}

	cfg.EditableExtensions = DefaultEditableExtensions
	if raw, set := lookup(getenv, "NEXTCLOUD_EDITABLE_EXTENSIONS"); set {
		cfg.EditableExtensions = nil
		for _, item := range splitList(raw) {
			item = strings.ToLower(item)
			if !strings.HasPrefix(item, ".") {
				item = "." + item
			}
			if strings.ContainsAny(item, "/\\*? ") || item == "." {
				fail("NEXTCLOUD_EDITABLE_EXTENSIONS has an invalid extension %q", item)
				continue
			}
			cfg.EditableExtensions = append(cfg.EditableExtensions, item)
		}
	}

	for _, pattern := range splitList(get("NEXTCLOUD_EDITABLE_SPREADSHEETS")) {
		pattern = strings.ToLower(strings.TrimLeft(pattern, "/"))
		if _, err := path.Match(pattern, ""); err != nil {
			fail("NEXTCLOUD_EDITABLE_SPREADSHEETS has an invalid pattern %q", pattern)
			continue
		}
		cfg.EditableSpreadsheets = append(cfg.EditableSpreadsheets, pattern)
	}
	for _, name := range splitList(get("NEXTCLOUD_PROTECTED_SHEETS")) {
		cfg.ProtectedSheets = append(cfg.ProtectedSheets, strings.ToLower(name))
	}

	// Groups.
	cfg.Groups = map[string]bool{}
	names := DefaultGroups
	if raw, set := lookup(getenv, "NEXTCLOUD_TOOL_GROUPS"); set {
		names = splitList(raw)
	}
	known := map[string]bool{}
	for _, name := range AllGroups {
		known[name] = true
	}
	for _, name := range names {
		name = strings.ToLower(name)
		if !known[name] {
			fail("NEXTCLOUD_TOOL_GROUPS has an unknown group %q (known: %s)", name, strings.Join(AllGroups, ", "))
			continue
		}
		cfg.Groups[name] = true
	}

	// Server.
	switch cfg.Transport {
	case "":
		cfg.Transport = TransportStdio
	case TransportStdio, TransportHTTP:
	case "http":
		cfg.Transport = TransportHTTP
	default:
		fail("MCP_TRANSPORT must be %q or %q", TransportStdio, TransportHTTP)
	}
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = ":8000"
	}

	if len(problems) > 0 {
		return nil, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	return cfg, nil
}

// EnabledGroups returns the enabled groups in display order.
func (c *Config) EnabledGroups() []string {
	var out []string
	for _, name := range AllGroups {
		if c.Groups[name] {
			out = append(out, name)
		}
	}
	return out
}

// ExtensionEditable reports whether text files with the extension of name may be created or edited.
func (c *Config) ExtensionEditable(name string) bool {
	extension := strings.ToLower(path.Ext(name))
	for _, allowed := range c.EditableExtensions {
		if extension == allowed {
			return true
		}
	}
	return false
}

// SpreadsheetEditable reports whether the spreadsheet at relativePath (relative
// to Root) matches one of the configured patterns. A pattern without "/" is
// matched against the file name; otherwise against the whole relative path.
func (c *Config) SpreadsheetEditable(relativePath string) bool {
	full := strings.ToLower(strings.TrimLeft(relativePath, "/"))
	base := path.Base(full)
	for _, pattern := range c.EditableSpreadsheets {
		candidate := base
		if strings.Contains(pattern, "/") {
			candidate = full
		}
		if ok, _ := path.Match(pattern, candidate); ok {
			return true
		}
	}
	return false
}

// SheetProtected reports whether a sheet name is in the protected list.
func (c *Config) SheetProtected(sheet string) bool {
	name := strings.ToLower(strings.TrimSpace(sheet))
	for _, protected := range c.ProtectedSheets {
		if name == protected {
			return true
		}
	}
	return false
}

// SortedExtensions returns the editable extensions sorted, for messages.
func (c *Config) SortedExtensions() []string {
	out := append([]string(nil), c.EditableExtensions...)
	sort.Strings(out)
	return out
}

func lookup(getenv Getenv, name string) (string, bool) {
	value := strings.TrimSpace(getenv(name))
	return value, value != ""
}

func splitList(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// cleanFolder normalises a folder setting to "/a/b" ("" when empty) and rejects "..".
func cleanFolder(raw string) (string, error) {
	var parts []string
	for _, part := range strings.Split(strings.ReplaceAll(raw, "\\", "/"), "/") {
		switch part {
		case "", ".":
		case "..":
			return "", errors.New("must not contain '..'")
		default:
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	return "/" + strings.Join(parts, "/"), nil
}
