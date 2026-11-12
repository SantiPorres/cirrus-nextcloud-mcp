package fakedav

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	trashPrefix    = "/remote.php/dav/trashbin/" + User + "/"
	versionsPrefix = "/remote.php/dav/versions/" + User + "/"
)

type trashItem struct {
	id       string
	location string           // original absolute path
	nodes    map[string]*Node // relative path ("" is the item itself) -> node
	deleted  time.Time
}

// subtree returns the paths at or below p.
func (s *Server) subtree(p string) []string {
	var paths []string
	for candidate := range s.nodes {
		if candidate == p || strings.HasPrefix(candidate, p+"/") {
			paths = append(paths, candidate)
		}
	}
	sort.Strings(paths)
	return paths
}

func (s *Server) destination(r *http.Request) (string, bool) {
	parsed, err := url.Parse(r.Header.Get("Destination"))
	const filesPrefix = "/remote.php/dav/files/" + User
	if err != nil || !strings.HasPrefix(parsed.Path, filesPrefix) {
		return "", false
	}
	return clean(strings.TrimPrefix(parsed.Path, filesPrefix)), true
}

func (s *Server) transfer(w http.ResponseWriter, r *http.Request, source string) {
	target, ok := s.destination(r)
	switch {
	case !ok:
		w.WriteHeader(http.StatusBadRequest)
		return
	case s.nodes[source] == nil || source == "":
		w.WriteHeader(http.StatusNotFound)
		return
	case s.nodes[parent(target)] == nil || !s.nodes[parent(target)].Dir:
		w.WriteHeader(http.StatusConflict)
		return
	}
	if s.nodes[target] != nil {
		if r.Header.Get("Overwrite") == "F" {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		s.unconditional++
		for _, p := range s.subtree(target) {
			delete(s.nodes, p)
		}
	}
	for _, p := range s.subtree(source) {
		node := s.nodes[p]
		moved := target + strings.TrimPrefix(p, source)
		if r.Method == "MOVE" {
			delete(s.nodes, p)
			s.nodes[moved] = node
			continue
		}
		s.nextID++
		duplicate := *node
		duplicate.ID, duplicate.ETag, duplicate.Versions = s.nextID, s.newETag(), nil
		duplicate.Data = append([]byte(nil), node.Data...)
		s.nodes[moved] = &duplicate
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) remove(w http.ResponseWriter, p string) {
	if s.nodes[p] == nil || p == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	item := &trashItem{location: p, nodes: map[string]*Node{}, deleted: time.Now()}
	for _, path := range s.subtree(p) {
		item.nodes[strings.TrimPrefix(path, p)] = s.nodes[path]
		delete(s.nodes, path)
	}
	if s.trashEnabled {
		s.version++
		item.id = fmt.Sprintf("%s.d%d", p[strings.LastIndex(p, "/")+1:], 1700000000+s.version)
		s.trash = append(s.trash, item)
	} else {
		s.permanent++
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) trashbin(w http.ResponseWriter, r *http.Request, rest string) {
	switch {
	case r.Method == "PROPFIND" && rest == "trash":
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns">`)
		b.WriteString(`<d:response><d:href>` + trashPrefix + `trash/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
		for _, item := range s.trash {
			node := item.nodes[""]
			resourceType, suffix := `<d:resourcetype/>`, ""
			if node.Dir {
				resourceType, suffix = `<d:resourcetype><d:collection/></d:resourcetype>`, "/"
			}
			location := strings.TrimPrefix(item.location, "/")
			fmt.Fprintf(&b, `<d:response><d:href>%strash/%s%s</d:href><d:propstat><d:prop>`, trashPrefix, escapeHref(item.id), suffix)
			fmt.Fprintf(&b, `<nc:trashbin-filename>%s</nc:trashbin-filename><nc:trashbin-original-location>%s</nc:trashbin-original-location>`,
				xmlEscape(location[strings.LastIndex(location, "/")+1:]), xmlEscape(location))
			fmt.Fprintf(&b, `<nc:trashbin-deletion-time>%d</nc:trashbin-deletion-time>%s<oc:fileid>%d</oc:fileid><d:getcontentlength>%d</d:getcontentlength>`,
				item.deleted.Unix(), resourceType, node.ID, len(node.Data))
			b.WriteString(`</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
		}
		b.WriteString(`</d:multistatus>`)
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, b.String())

	case r.Method == "MOVE" && strings.HasPrefix(rest, "trash/"):
		id, _ := url.PathUnescape(strings.TrimPrefix(rest, "trash/"))
		destination, _ := url.Parse(r.Header.Get("Destination"))
		if destination == nil || !strings.HasPrefix(destination.Path, trashPrefix+"restore/") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for index, item := range s.trash {
			if item.id != id {
				continue
			}
			location := item.location
			if s.nodes[location] != nil {
				location += " (restored)"
			}
			s.addDir(parent(location))
			for rel, node := range item.nodes {
				s.nodes[location+rel] = node
			}
			s.trash = append(s.trash[:index], s.trash[index+1:]...)
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusNotFound)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) nodeByID(id string) *Node {
	for _, node := range s.nodes {
		if strconv.Itoa(node.ID) == id && !node.Dir {
			return node
		}
	}
	return nil
}

func (s *Server) versions(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] != "versions" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	node := s.nodeByID(parts[1])
	if node == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	switch {
	case r.Method == "PROPFIND" && len(parts) == 2:
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:nc="http://nextcloud.org/ns">`)
		fmt.Fprintf(&b, `<d:response><d:href>%sversions/%s/</d:href><d:propstat><d:prop><d:getlastmodified/></d:prop><d:status>HTTP/1.1 404 Not Found</d:status></d:propstat></d:response>`, versionsPrefix, parts[1])
		for _, version := range node.Versions {
			fmt.Fprintf(&b, `<d:response><d:href>%sversions/%s/%s</d:href><d:propstat><d:prop>`, versionsPrefix, parts[1], version.ID)
			fmt.Fprintf(&b, `<d:getlastmodified>%s</d:getlastmodified><d:getcontentlength>%d</d:getcontentlength><nc:version-author>%s</nc:version-author>`,
				version.Modified.UTC().Format(http.TimeFormat), len(version.Data), User)
			b.WriteString(`</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>`)
			b.WriteString(`<d:propstat><d:prop><nc:version-label/></d:prop><d:status>HTTP/1.1 404 Not Found</d:status></d:propstat></d:response>`)
		}
		b.WriteString(`</d:multistatus>`)
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, b.String())

	case r.Method == "MOVE" && len(parts) == 3:
		for _, version := range node.Versions {
			if version.ID == parts[2] {
				s.replace(node, version.Data)
				w.WriteHeader(http.StatusCreated)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) capabilities(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ocs": map[string]any{
		"meta": map[string]any{"status": "ok", "statuscode": 200, "message": "OK"},
		"data": map[string]any{
			"version": map[string]any{"string": s.NextcloudVersion},
			"capabilities": map[string]any{
				"core":  map[string]any{},
				"files": map[string]any{"undelete": s.trashEnabled, "versioning": true},
			},
		},
	}})
}

func xmlEscape(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
}
