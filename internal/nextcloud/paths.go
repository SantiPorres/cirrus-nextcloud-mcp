package nextcloud

import (
	"net/url"
	"regexp"
	"strings"
)

// Target is a file or folder the user pointed at, by path or by link.
type Target struct {
	// Abs is the absolute path in the account, e.g. "/Team/Specs/a.md".
	Abs string
	// Rel is the path relative to the configured root, e.g. "/Specs/a.md".
	Rel string
	// Name is the last path segment.
	Name string
}

// splitPath turns a user-supplied path into clean segments. Backslashes count
// as separators; "" and "." are dropped; ".." and control characters are rejected.
func splitPath(userPath string) ([]string, error) {
	var parts []string
	for _, part := range strings.Split(strings.ReplaceAll(userPath, "\\", "/"), "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			return nil, Errorf("Paths containing '..' are not allowed.")
		}
		for _, r := range part {
			if r < 0x20 || r == 0x7f {
				return nil, Errorf("Paths must not contain control characters.")
			}
		}
		parts = append(parts, part)
	}
	return parts, nil
}

// Abs turns a path relative to the root into an absolute path in the account.
// The result is "" for the top of an unrestricted account.
func (c *Client) Abs(userPath string) (string, error) {
	parts, err := splitPath(userPath)
	if err != nil {
		return "", err
	}
	if len(parts) == 0 {
		return c.cfg.Root, nil
	}
	return c.cfg.Root + "/" + strings.Join(parts, "/"), nil
}

// Rel is the inverse of Abs. It reports false when abs is outside the root.
func (c *Client) Rel(abs string) (string, bool) {
	abs = strings.TrimRight(abs, "/")
	root := c.cfg.Root
	switch {
	case root == "":
		if abs == "" {
			return "/", true
		}
		return abs, true
	case abs == root:
		return "/", true
	case strings.HasPrefix(abs, root+"/"):
		return abs[len(root):], true
	}
	return "", false
}

// PathTarget resolves a path (relative to the root) without contacting Nextcloud.
// It refuses the top folder when needFile is true.
func (c *Client) PathTarget(userPath string, needFile bool) (Target, error) {
	if IsLink(userPath) {
		return Target{}, Errorf("Provide a path here, not a link.")
	}
	abs, err := c.Abs(userPath)
	if err != nil {
		return Target{}, err
	}
	rel, _ := c.Rel(abs)
	if needFile && rel == "/" {
		return Target{}, Errorf("The path must point to a file, not to the top folder.")
	}
	return Target{Abs: abs, Rel: rel, Name: lastSegment(rel)}, nil
}

func lastSegment(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	if p == "" {
		return "/"
	}
	return p
}

// IsLink reports whether a target looks like a URL rather than a path.
func IsLink(target string) bool {
	lower := strings.ToLower(strings.TrimSpace(target))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

var fileIDLink = regexp.MustCompile(`^/(?:index\.php/)?(?:f|apps/files/files)/(\d+)/?$`)

// ParseFileLink extracts the file id from an internal link such as
// https://<host>/f/<id>. Only links on the configured Nextcloud are accepted;
// public share links (/s/<token>) are not supported.
func (c *Client) ParseFileLink(link string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", Errorf("Not a valid link.")
	}
	base := c.cfg.BaseURL
	if !strings.EqualFold(parsed.Host, base.Host) {
		return "", Errorf("The link must belong to %s.", base.Host)
	}
	path := parsed.Path
	if prefix := strings.TrimRight(base.Path, "/"); prefix != "" {
		if !strings.HasPrefix(path, prefix+"/") {
			return "", Errorf("The link must belong to %s.", c.base)
		}
		path = path[len(prefix):]
	}
	if m := fileIDLink.FindStringSubmatch(path); m != nil {
		return m[1], nil
	}
	return "", Errorf("Unsupported link. Expected an internal file link such as %s/f/<id>.", c.base)
}

// FileLink returns the internal link of a file id.
func (c *Client) FileLink(fileID string) string {
	if fileID == "" {
		return ""
	}
	return c.base + "/f/" + fileID
}
