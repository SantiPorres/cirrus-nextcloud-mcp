package fakedav

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type fakeTag struct {
	id   int
	name string
}

type fakeComment struct {
	id      int
	message string
	created time.Time
}

// FakeShare is a share stored by the fake.
type FakeShare struct {
	ID          int
	Type        int
	Path        string
	ShareWith   string
	Password    string
	Expiration  string
	Permissions int
	Label       string
}

// FakeActivity is an entry of the fake activity stream.
type FakeActivity struct {
	Type       string
	Subject    string
	ObjectType string
	ObjectName string
}

type metaState struct {
	tags       []fakeTag
	relations  map[int]map[int]bool // file id -> tag ids
	comments   map[int][]fakeComment
	shares     []*FakeShare
	activities []FakeActivity
	counter    int
}

func (m *metaState) next() int {
	m.counter++
	return m.counter
}

// AddActivity appends an entry to the activity stream (the last added is the newest).
func (s *Server) AddActivity(activity FakeActivity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meta.activities = append(s.meta.activities, activity)
}

// AddShare stores a share directly, as another client of the same account would.
func (s *Server) AddShare(share FakeShare) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	share.ID = s.meta.next()
	s.meta.shares = append(s.meta.shares, &share)
	return share.ID
}

// Shares returns a copy of the stored shares.
func (s *Server) Shares() []FakeShare {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]FakeShare, 0, len(s.meta.shares))
	for _, share := range s.meta.shares {
		out = append(out, *share)
	}
	return out
}

func ocsWrite(w http.ResponseWriter, status int, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ocs": map[string]any{
		"meta": map[string]any{"status": "x", "statuscode": status, "message": message}, "data": data,
	}})
}

const (
	tagsPath      = "/remote.php/dav/systemtags/"
	relationsPath = "/remote.php/dav/systemtags-relations/files/"
	commentsPath  = "/remote.php/dav/comments/files/"
	sharesPath    = "/ocs/v2.php/apps/files_sharing/api/v1/shares"
)

// serveMeta handles the metadata and sharing endpoints; false when the path is not one of them.
func (s *Server) serveMeta(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	switch {
	case path == tagsPath:
		s.serveTags(w, r)
	case strings.HasPrefix(path, relationsPath):
		s.serveRelations(w, r, strings.Trim(strings.TrimPrefix(path, relationsPath), "/"))
	case strings.HasPrefix(path, commentsPath):
		s.serveComments(w, r, strings.Trim(strings.TrimPrefix(path, commentsPath), "/"))
	case path == "/ocs/v2.php/cloud/user":
		ocsWrite(w, http.StatusOK, "OK", map[string]any{
			"id": User, "displayname": "Alice Example", "email": nil, "groups": []string{"staff"}, "language": "en",
			"quota": map[string]any{"free": 900, "used": 100, "total": 1000, "relative": 10.0},
		})
	case path == "/ocs/v2.php/apps/activity/api/v2/activity":
		s.serveActivity(w, r)
	case path == sharesPath || strings.HasPrefix(path, sharesPath+"/"):
		s.serveShares(w, r, strings.Trim(strings.TrimPrefix(path, sharesPath), "/"))
	default:
		return false
	}
	return true
}

func tagXML(b *strings.Builder, href string, tag fakeTag) {
	fmt.Fprintf(b, `<d:response><d:href>%s</d:href><d:propstat><d:prop><oc:id>%d</oc:id><oc:display-name>%s</oc:display-name>`+
		`<oc:user-visible>true</oc:user-visible><oc:user-assignable>true</oc:user-assignable></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`,
		href, tag.id, xmlEscape(tag.name))
}

func writeMultistatus(w http.ResponseWriter, collectionHref string, body string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`+
		`<d:response><d:href>`+collectionHref+`</d:href><d:propstat><d:prop><oc:id/></d:prop><d:status>HTTP/1.1 404 Not Found</d:status></d:propstat></d:response>`+
		body+`</d:multistatus>`)
}

func (s *Server) serveTags(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, tag := range s.meta.tags {
			if tag.name == body.Name {
				w.WriteHeader(http.StatusConflict)
				return
			}
		}
		tag := fakeTag{id: s.meta.next(), name: body.Name}
		s.meta.tags = append(s.meta.tags, tag)
		w.Header().Set("Content-Location", tagsPath+strconv.Itoa(tag.id))
		w.WriteHeader(http.StatusCreated)
	case "PROPFIND":
		var b strings.Builder
		for _, tag := range s.meta.tags {
			tagXML(&b, tagsPath+strconv.Itoa(tag.id)+"/", tag)
		}
		writeMultistatus(w, tagsPath, b.String())
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) serveRelations(w http.ResponseWriter, r *http.Request, rest string) {
	fileRaw, tagRaw, hasTag := strings.Cut(rest, "/")
	fileID, _ := strconv.Atoi(fileRaw)
	tagID, _ := strconv.Atoi(tagRaw)
	if s.meta.relations == nil {
		s.meta.relations = map[int]map[int]bool{}
	}
	assigned := s.meta.relations[fileID]
	switch {
	case r.Method == "PROPFIND" && !hasTag:
		var b strings.Builder
		for _, tag := range s.meta.tags {
			if assigned[tag.id] {
				tagXML(&b, relationsPath+fileRaw+"/"+strconv.Itoa(tag.id), tag)
			}
		}
		writeMultistatus(w, relationsPath+fileRaw+"/", b.String())
	case r.Method == http.MethodPut && hasTag:
		known := false
		for _, tag := range s.meta.tags {
			known = known || tag.id == tagID
		}
		switch {
		case !known:
			w.WriteHeader(http.StatusNotFound)
		case assigned[tagID]:
			w.WriteHeader(http.StatusConflict)
		default:
			if assigned == nil {
				assigned = map[int]bool{}
				s.meta.relations[fileID] = assigned
			}
			assigned[tagID] = true
			w.WriteHeader(http.StatusCreated)
		}
	case r.Method == http.MethodDelete && hasTag:
		if !assigned[tagID] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(assigned, tagID)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

var (
	tagRule    = regexp.MustCompile(`<oc:systemtag>(\d+)</oc:systemtag>`)
	limitRule  = regexp.MustCompile(`<oc:limit>(\d+)</oc:limit>`)
	offsetRule = regexp.MustCompile(`<oc:offset>(\d+)</oc:offset>`)
)

// filterFiles answers the filter-files REPORT on a folder: by tag or favorites, within that folder.
func (s *Server) filterFiles(w http.ResponseWriter, r *http.Request, scope string) {
	raw, _ := io.ReadAll(r.Body)
	body := string(raw)
	tagID := 0
	if m := tagRule.FindStringSubmatch(body); m != nil {
		tagID, _ = strconv.Atoi(m[1])
	}
	favorites := strings.Contains(body, "<oc:favorite>1</oc:favorite>")
	var paths []string
	for p, node := range s.nodes {
		if p == "" || (scope != "" && !strings.HasPrefix(p, scope+"/")) {
			continue
		}
		if (tagID != 0 && s.meta.relations[node.ID][tagID]) || (favorites && node.Favorite) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	s.multistatus(w, paths)
}

func (s *Server) serveComments(w http.ResponseWriter, r *http.Request, fileRaw string) {
	fileID, _ := strconv.Atoi(fileRaw)
	if s.meta.comments == nil {
		s.meta.comments = map[int][]fakeComment{}
	}
	switch r.Method {
	case http.MethodPost:
		var body struct {
			Message string `json:"message"`
			Verb    string `json:"verb"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Verb != "comment" || body.Message == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.meta.comments[fileID] = append(s.meta.comments[fileID], fakeComment{id: s.meta.next(), message: body.Message, created: time.Now()})
		w.WriteHeader(http.StatusCreated)
	case "REPORT":
		raw, _ := io.ReadAll(r.Body)
		limit, offset := 1000, 0
		if m := limitRule.FindStringSubmatch(string(raw)); m != nil {
			limit, _ = strconv.Atoi(m[1])
		}
		if m := offsetRule.FindStringSubmatch(string(raw)); m != nil {
			offset, _ = strconv.Atoi(m[1])
		}
		all := s.meta.comments[fileID]
		var b strings.Builder
		for index := len(all) - 1 - offset; index >= 0 && limit > 0; index, limit = index-1, limit-1 {
			comment := all[index]
			fmt.Fprintf(&b, `<d:response><d:href>%s%s/%d</d:href><d:propstat><d:prop><oc:id>%d</oc:id><oc:message>%s</oc:message>`+
				`<oc:actorId>%s</oc:actorId><oc:actorDisplayName>Alice Example</oc:actorDisplayName><oc:creationDateTime>%s</oc:creationDateTime>`+
				`</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`,
				commentsPath, fileRaw, comment.id, comment.id, xmlEscape(comment.message), User, comment.created.UTC().Format(http.TimeFormat))
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`+b.String()+`</d:multistatus>`)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) serveActivity(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	var data []map[string]any
	for index := len(s.meta.activities) - 1; index >= 0 && len(data) < limit; index-- {
		id := index + 1
		if since > 0 && id >= since {
			continue
		}
		activity := s.meta.activities[index]
		data = append(data, map[string]any{
			"activity_id": id, "type": activity.Type, "user": User, "subject": activity.Subject,
			"object_type": activity.ObjectType, "object_name": activity.ObjectName, "datetime": "2026-01-01T00:00:00+00:00",
		})
	}
	if len(data) == 0 {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	ocsWrite(w, http.StatusOK, "OK", data)
}

func (s *Server) shareJSON(share *FakeShare) map[string]any {
	itemType := "file"
	if node := s.nodes[clean(share.Path)]; node != nil && node.Dir {
		itemType = "folder"
	}
	out := map[string]any{
		"id": strconv.Itoa(share.ID), "share_type": share.Type, "path": share.Path, "item_type": itemType,
		"permissions": share.Permissions, "label": share.Label, "stime": 1767225600, "expiration": nil, "share_with": nil,
	}
	if share.Expiration != "" {
		out["expiration"] = share.Expiration + " 00:00:00"
	}
	if share.Type == 3 {
		out["url"] = s.URL + "/s/token" + strconv.Itoa(share.ID)
		if share.Password != "" {
			// Like Nextcloud, links report the password hash in share_with.
			out["share_with"], out["password"] = "3|$argon2id$hash-of-password", "3|$argon2id$hash-of-password"
		}
	} else {
		out["share_with"] = share.ShareWith
	}
	return out
}

func (s *Server) serveShares(w http.ResponseWriter, r *http.Request, idRaw string) {
	find := func() (int, *FakeShare) {
		for index, share := range s.meta.shares {
			if strconv.Itoa(share.ID) == idRaw {
				return index, share
			}
		}
		return -1, nil
	}
	switch {
	case r.Method == http.MethodGet && idRaw == "":
		wanted := r.URL.Query().Get("path")
		data := []map[string]any{}
		for _, share := range s.meta.shares {
			if wanted == "" || clean(wanted) == clean(share.Path) {
				data = append(data, s.shareJSON(share))
			}
		}
		ocsWrite(w, http.StatusOK, "OK", data)

	case r.Method == http.MethodPost && idRaw == "":
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		share := &FakeShare{Path: form.Get("path"), ShareWith: form.Get("shareWith"), Password: form.Get("password"),
			Expiration: form.Get("expireDate"), Label: form.Get("label")}
		share.Type, _ = strconv.Atoi(form.Get("shareType"))
		share.Permissions, _ = strconv.Atoi(form.Get("permissions"))
		switch {
		case s.nodes[clean(share.Path)] == nil:
			ocsWrite(w, http.StatusNotFound, "Wrong path, file/folder does not exist", nil)
			return
		case share.Type == 0 && share.ShareWith != "bob":
			ocsWrite(w, http.StatusNotFound, "Please specify a valid account to share with", nil)
			return
		case share.Password == "password-too-common":
			ocsWrite(w, http.StatusBadRequest, "Password password-too-common is among the most common ones", nil)
			return
		}
		share.ID = s.meta.next()
		s.meta.shares = append(s.meta.shares, share)
		ocsWrite(w, http.StatusOK, "OK", s.shareJSON(share))

	case r.Method == http.MethodGet:
		if _, share := find(); share != nil {
			ocsWrite(w, http.StatusOK, "OK", []map[string]any{s.shareJSON(share)})
			return
		}
		ocsWrite(w, http.StatusNotFound, "Wrong share ID, share does not exist", nil)

	case r.Method == http.MethodDelete:
		if index, share := find(); share != nil {
			s.meta.shares = append(s.meta.shares[:index], s.meta.shares[index+1:]...)
			ocsWrite(w, http.StatusOK, "OK", []any{})
			return
		}
		ocsWrite(w, http.StatusNotFound, "Wrong share ID, share does not exist", nil)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
