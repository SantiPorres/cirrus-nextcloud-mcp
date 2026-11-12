package config

import (
	"fmt"
	"strings"
	"testing"
)

func env(pairs map[string]string) Getenv {
	return func(name string) string { return pairs[name] }
}

func base(extra map[string]string) map[string]string {
	out := map[string]string{
		"NEXTCLOUD_URL":          "https://cloud.example.com/",
		"NEXTCLOUD_USER":         "alice",
		"NEXTCLOUD_APP_PASSWORD": "s3cr3t-value",
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func TestDefaultsAreReadOnly(t *testing.T) {
	cfg, err := Load(env(base(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AllowWrite || cfg.AllowDelete || cfg.AllowShare {
		t.Error("write, delete and share must be off by default")
	}
	if cfg.Groups[GroupSharing] {
		t.Error("the sharing group must be off by default")
	}
	if !cfg.Groups[GroupFiles] || !cfg.Groups[GroupDiagnostics] {
		t.Error("files and diagnostics should be on by default")
	}
	if !cfg.ShareRequirePassword || !cfg.ShareRequireExpiry {
		t.Error("share links must require password and expiry by default")
	}
	if cfg.Transport != TransportStdio || cfg.Root != "" || cfg.FolderWritePrefix != "" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.BaseURL.String() != "https://cloud.example.com" {
		t.Errorf("base URL = %s", cfg.BaseURL)
	}
	if len(cfg.EditableSpreadsheets) != 0 {
		t.Error("no spreadsheet may be editable by default")
	}
}

func TestInvalidConfigurations(t *testing.T) {
	cases := map[string]map[string]string{
		"missing credentials":  {"NEXTCLOUD_APP_PASSWORD": ""},
		"http without opt-in":  {"NEXTCLOUD_URL": "http://cloud.example.com"},
		"credentials in URL":   {"NEXTCLOUD_URL": "https://bob:pw@cloud.example.com"},
		"other scheme":         {"NEXTCLOUD_URL": "ftp://cloud.example.com"},
		"dotdot in root":       {"NEXTCLOUD_ROOT": "/Team/../Other"},
		"dotdot in prefix":     {"NEXTCLOUD_FOLDER_WRITE_PREFIX": "../x"},
		"unknown group":        {"NEXTCLOUD_TOOL_GROUPS": "files,nope"},
		"bad boolean":          {"NEXTCLOUD_ALLOW_WRITE": "maybe"},
		"bad transport":        {"MCP_TRANSPORT": "carrier-pigeon"},
		"bad extension":        {"NEXTCLOUD_EDITABLE_EXTENSIONS": "md,*"},
		"bad spreadsheet glob": {"NEXTCLOUD_EDITABLE_SPREADSHEETS": "[oops"},
		"bad number":           {"NEXTCLOUD_MAX_TEXT_BYTES": "0"},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(env(base(extra)))
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), "s3cr3t-value") || strings.Contains(err.Error(), "bob:pw") {
				t.Errorf("error leaks a secret: %v", err)
			}
		})
	}
}

func TestHTTPNeedsExplicitOptIn(t *testing.T) {
	cfg, err := Load(env(base(map[string]string{
		"NEXTCLOUD_URL": "http://localhost:8080", "NEXTCLOUD_ALLOW_INSECURE_HTTP": "true",
	})))
	if err != nil || cfg.BaseURL.Scheme != "http" {
		t.Fatalf("cfg=%v err=%v", cfg, err)
	}
}

func TestScopeSettings(t *testing.T) {
	cfg, err := Load(env(base(map[string]string{
		"NEXTCLOUD_ROOT":                  "Team\\Docs/",
		"NEXTCLOUD_FOLDER_WRITE_PREFIX":   "/Projects/./Meetings",
		"NEXTCLOUD_EDITABLE_EXTENSIONS":   "MD, .txt",
		"NEXTCLOUD_EDITABLE_SPREADSHEETS": "Decisions*.xlsx, /Plans/*/matrix.xlsx",
		"NEXTCLOUD_PROTECTED_SHEETS":      "Guide, README",
		"NEXTCLOUD_TOOL_GROUPS":           "files, SHARING",
		"NEXTCLOUD_ALLOW_WRITE":           "yes",
		"MCP_TRANSPORT":                   "http",
	})))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Root != "/Team/Docs" || cfg.FolderWritePrefix != "/Projects/Meetings" {
		t.Errorf("root=%q prefix=%q", cfg.Root, cfg.FolderWritePrefix)
	}
	if !cfg.ExtensionEditable("Notes.MD") || !cfg.ExtensionEditable("a.txt") || cfg.ExtensionEditable("a.json") || cfg.ExtensionEditable("noext") {
		t.Error("editable extensions not applied")
	}
	for path, want := range map[string]bool{
		"/Any/Folder/Decisions 2026.xlsx": true,
		"/Plans/Q1/Matrix.xlsx":           true,
		"/Plans/Q1/deep/matrix.xlsx":      false,
		"/matrix.xlsx":                    false,
		"/other.xlsx":                     false,
	} {
		if got := cfg.SpreadsheetEditable(path); got != want {
			t.Errorf("SpreadsheetEditable(%q) = %v, want %v", path, got, want)
		}
	}
	if !cfg.SheetProtected(" guide ") || cfg.SheetProtected("Data") {
		t.Error("protected sheets not applied")
	}
	if got := strings.Join(cfg.EnabledGroups(), ","); got != "files,sharing" {
		t.Errorf("groups = %s", got)
	}
	if !cfg.AllowWrite || cfg.Transport != TransportHTTP {
		t.Error("flags not applied")
	}
}

func TestSecretNeverPrints(t *testing.T) {
	cfg, err := Load(env(base(nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		fmt.Sprint(cfg.AppPassword), fmt.Sprintf("%v %+v %#v %s", cfg, cfg, cfg.AppPassword, cfg.AppPassword),
	} {
		if strings.Contains(text, "s3cr3t-value") {
			t.Errorf("secret printed: %s", text)
		}
	}
	if cfg.AppPassword.Reveal() != "s3cr3t-value" {
		t.Error("Reveal must return the value")
	}
}
