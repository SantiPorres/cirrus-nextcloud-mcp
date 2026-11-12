// Package fakedav is an in-memory stand-in for the parts of Nextcloud the
// client uses, for unit tests. It is strict where it matters for safety: it
// records every request and flags any PUT that would overwrite a file
// without a precondition.
package fakedav

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Default credentials of the fake account.
const (
	User     = "alice"
	Password = "app-password-s3cr3t"
)

// Node is a file or folder.
type Node struct {
	ID          int
	Dir         bool
	Data        []byte
	ETag        string
	Modified    time.Time
	Permissions string
	// Versions are the previous contents, oldest first.
	Versions []Version
}

// Version is a previous content of a file.
type Version struct {
	ID       string
	Data     []byte
	Modified time.Time
}

// Request is one recorded request.
type Request struct {
	Method string
	Path   string
	Header http.Header
}

// Server is the fake Nextcloud.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	nodes    map[string]*Node // absolute path ("" is the home folder) -> node
	nextID   int
	version  int
	requests []Request
	// unconditional counts PUTs that replaced an existing file without If-Match.
	unconditional int
	beforePut     func()

	// trashEnabled mirrors the files_trashbin app. When false, DELETE destroys data.
	trashEnabled bool
	trash        []*trashItem
	// permanent counts items destroyed by a DELETE while the trash was disabled.
	permanent int
	// NextcloudVersion is reported by the capabilities endpoint.
	NextcloudVersion string

	sessions map[string]map[int][]byte // upload id -> chunk index -> data
	previews map[int][]byte            // file id -> preview image
}

// New starts a fake server that is closed when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{nodes: map[string]*Node{}, nextID: 100, trashEnabled: true, NextcloudVersion: "33.0.0",
		sessions: map[string]map[int][]byte{}, previews: map[int][]byte{}}
	s.nodes[""] = &Node{ID: 1, Dir: true, Modified: time.Now(), Permissions: "RGDNVCK"}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func clean(p string) string { return strings.TrimRight("/"+strings.Trim(p, "/"), "/") }

func parent(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return ""
}

func (s *Server) newETag() string {
	s.version++
	return fmt.Sprintf(`"etag-%d"`, s.version)
}

// AddDir creates a folder and its missing parents.
func (s *Server) AddDir(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addDir(clean(p))
}

func (s *Server) addDir(p string) {
	if p == "" || s.nodes[p] != nil {
		return
	}
	s.addDir(parent(p))
	s.nextID++
	s.nodes[p] = &Node{ID: s.nextID, Dir: true, ETag: s.newETag(), Modified: time.Now(), Permissions: "RGDNVCK"}
}

// AddFile creates or replaces a file (creating parent folders) and returns its id.
func (s *Server) AddFile(p string, data []byte) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	p = clean(p)
	s.addDir(parent(p))
	if node := s.nodes[p]; node != nil {
		s.replace(node, data)
		return node.ID
	}
	s.nextID++
	s.nodes[p] = &Node{ID: s.nextID, Data: data, ETag: s.newETag(), Modified: time.Now(), Permissions: "RGDNVW"}
	return s.nextID
}

// replace sets new content, keeping the old one as a version, like Nextcloud does.
func (s *Server) replace(node *Node, data []byte) {
	s.version++
	node.Versions = append(node.Versions, Version{
		ID: strconv.Itoa(1700000000 + s.version), Data: node.Data, Modified: node.Modified,
	})
	node.Data, node.ETag, node.Modified = data, s.newETag(), time.Now()
}

// DisableTrash makes the fake behave like a Nextcloud without the trash bin app.
func (s *Server) DisableTrash() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trashEnabled = false
}

// DeleteToTrash deletes a path directly, as another client of the same account would.
func (s *Server) DeleteToTrash(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remove(httptest.NewRecorder(), clean(p))
}

// TrashIDs returns the ids of everything in the trash, oldest first.
func (s *Server) TrashIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.trash))
	for _, item := range s.trash {
		ids = append(ids, item.id)
	}
	return ids
}

// PermanentDeletes is how many items a DELETE destroyed for good. It must stay 0.
func (s *Server) PermanentDeletes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.permanent
}

// File returns the content of a file and whether it exists.
func (s *Server) File(p string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node := s.nodes[clean(p)]
	if node == nil || node.Dir {
		return nil, false
	}
	return node.Data, true
}

// Exists reports whether a file or folder exists.
func (s *Server) Exists(p string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nodes[clean(p)] != nil
}

// BeforeNextPut runs fn once, just before the next PUT is processed. Use it
// to simulate someone else changing a file between a read and a write.
func (s *Server) BeforeNextPut(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.beforePut = fn
}

// Requests returns a copy of the recorded requests.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// UnconditionalOverwrites is how many PUTs replaced an existing file without If-Match. It must stay 0.
func (s *Server) UnconditionalOverwrites() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unconditional
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone()})
	hook := s.beforePut
	if r.Method == http.MethodPut {
		s.beforePut = nil
	}
	s.mu.Unlock()

	user, password, ok := r.BasicAuth()
	if !ok || user != User || password != Password {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodPut && hook != nil {
		hook()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	const filesPrefix = "/remote.php/dav/files/" + User
	switch {
	case r.URL.Path == "/remote.php/dav/" && r.Method == "SEARCH":
		s.search(w, r)
	case r.URL.Path == "/ocs/v2.php/cloud/capabilities":
		s.capabilities(w)
	case r.URL.Path == "/index.php/core/preview":
		s.preview(w, r)
	case strings.HasPrefix(r.URL.Path, uploadsPrefix):
		s.uploads(w, r, strings.Trim(strings.TrimPrefix(r.URL.Path, uploadsPrefix), "/"))
	case strings.HasPrefix(r.URL.Path, trashPrefix):
		s.trashbin(w, r, strings.Trim(strings.TrimPrefix(r.URL.Path, trashPrefix), "/"))
	case strings.HasPrefix(r.URL.Path, versionsPrefix):
		s.versions(w, r, strings.Trim(strings.TrimPrefix(r.URL.Path, versionsPrefix), "/"))
	case r.URL.Path == filesPrefix || strings.HasPrefix(r.URL.Path, filesPrefix+"/"):
		s.files(w, r, clean(strings.TrimPrefix(r.URL.Path, filesPrefix)))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *Server) files(w http.ResponseWriter, r *http.Request, p string) {
	node := s.nodes[p]
	switch r.Method {
	case "PROPFIND":
		if node == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		paths := []string{p}
		if node.Dir && r.Header.Get("Depth") == "1" {
			for candidate := range s.nodes {
				if candidate != p && parent(candidate) == p {
					paths = append(paths, candidate)
				}
			}
			sort.Strings(paths[1:])
		}
		s.multistatus(w, paths)

	case http.MethodGet:
		if node == nil || node.Dir {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if ifMatch := r.Header.Get("If-Match"); ifMatch != "" && ifMatch != node.ETag {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		w.Header().Set("ETag", node.ETag)
		w.Header().Set("Content-Type", contentType(p))
		data := node.Data
		if m := rangePattern.FindStringSubmatch(r.Header.Get("Range")); m != nil {
			from, _ := strconv.Atoi(m[1])
			to, _ := strconv.Atoi(m[2])
			if from >= len(data) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			to = min(to, len(data)-1)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to, len(data)))
			w.Header().Set("Content-Length", strconv.Itoa(to-from+1))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[from : to+1])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)

	case http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		if parentNode := s.nodes[parent(p)]; parentNode == nil || !parentNode.Dir {
			w.WriteHeader(http.StatusNotFound) // what Nextcloud answers, rather than 409
			return
		}
		ifMatch, ifNoneMatch := r.Header.Get("If-Match"), r.Header.Get("If-None-Match")
		if node != nil {
			if node.Dir {
				w.WriteHeader(http.StatusConflict)
				return
			}
			if ifNoneMatch == "*" || (ifMatch != "" && ifMatch != node.ETag) {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			if ifMatch == "" {
				s.unconditional++
			}
			s.replace(node, data)
			w.Header().Set("ETag", node.ETag)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if ifMatch != "" {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		s.nextID++
		created := &Node{ID: s.nextID, Data: data, ETag: s.newETag(), Modified: time.Now(), Permissions: "RGDNVW"}
		s.nodes[p] = created
		w.Header().Set("OC-ETag", created.ETag)
		w.WriteHeader(http.StatusCreated)

	case "MKCOL":
		switch {
		case node != nil:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case s.nodes[parent(p)] == nil || !s.nodes[parent(p)].Dir:
			w.WriteHeader(http.StatusConflict)
		default:
			s.addDir(p)
			w.WriteHeader(http.StatusCreated)
		}

	case "MOVE", "COPY":
		s.transfer(w, r, p)

	case http.MethodDelete:
		s.remove(w, p)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

var rangePattern = regexp.MustCompile(`^bytes=(\d+)-(\d+)$`)

func contentType(p string) string {
	switch strings.ToLower(p[strings.LastIndex(p, ".")+1:]) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "pdf":
		return "application/pdf"
	case "md", "txt":
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

var (
	scopePattern   = regexp.MustCompile(`<d:href>([^<]*)</d:href>`)
	literalPattern = regexp.MustCompile(`<d:literal>([^<]*)</d:literal>`)
	limitPattern   = regexp.MustCompile(`<d:nresults>(\d+)</d:nresults>`)
)

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	body := string(raw)
	scope, literal, limit := "", "", 1000
	if m := scopePattern.FindStringSubmatch(body); m != nil {
		scope = clean(strings.TrimPrefix(m[1], "/files/"+User))
	}
	if m := literalPattern.FindStringSubmatch(body); m != nil {
		literal = m[1]
	}
	if m := limitPattern.FindStringSubmatch(body); m != nil {
		limit, _ = strconv.Atoi(m[1])
	}
	byID := strings.Contains(body, "<oc:fileid/></d:prop><d:literal>")

	var paths []string
	for p, node := range s.nodes {
		if p == "" || (scope != "" && p != scope && !strings.HasPrefix(p, scope+"/")) {
			continue
		}
		if byID {
			if strconv.Itoa(node.ID) == literal {
				paths = append(paths, p)
			}
			continue
		}
		name := strings.ToLower(p[strings.LastIndex(p, "/")+1:])
		if strings.Contains(name, strings.ToLower(strings.Trim(literal, "%"))) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	if len(paths) > limit {
		paths = paths[:limit]
	}
	s.multistatus(w, paths)
}

func escapeHref(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func (s *Server) multistatus(w http.ResponseWriter, paths []string) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns">`)
	for _, p := range paths {
		node := s.nodes[p]
		href := "/remote.php/dav/files/" + User + "/"
		if p != "" {
			href += escapeHref(p)
			if node.Dir {
				href += "/"
			}
		}
		b.WriteString(`<d:response><d:href>` + href + `</d:href><d:propstat><d:prop>`)
		fmt.Fprintf(&b, `<d:getlastmodified>%s</d:getlastmodified><d:getetag>%s</d:getetag>`,
			node.Modified.UTC().Format(http.TimeFormat), strings.ReplaceAll(node.ETag, `"`, "&quot;"))
		fmt.Fprintf(&b, `<oc:fileid>%d</oc:fileid><oc:permissions>%s</oc:permissions><oc:favorite>0</oc:favorite>`, node.ID, node.Permissions)
		if node.Dir {
			b.WriteString(`<d:resourcetype><d:collection/></d:resourcetype><oc:size>0</oc:size>`)
			// Like Nextcloud, report the properties a folder lacks in a 404 block.
			b.WriteString(`</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>`)
			b.WriteString(`<d:propstat><d:prop><d:getcontentlength/><d:getcontenttype/></d:prop><d:status>HTTP/1.1 404 Not Found</d:status></d:propstat>`)
		} else {
			fmt.Fprintf(&b, `<d:resourcetype/><d:getcontentlength>%d</d:getcontentlength><oc:size>%d</oc:size>`, len(node.Data), len(node.Data))
			b.WriteString(`<d:getcontenttype>application/octet-stream</d:getcontenttype>`)
			b.WriteString(`</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>`)
		}
		b.WriteString(`</d:response>`)
	}
	b.WriteString(`</d:multistatus>`)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(w, b.String())
}
