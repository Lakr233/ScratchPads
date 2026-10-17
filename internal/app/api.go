package app

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxFormBody = 64 << 10

// params merges query parameters with a JSON object or form body so every
// endpoint accepts `-d key=value`, `--json '{...}'` and `?key=value` alike.
func params(r *http.Request) (url.Values, error) {
	values := r.URL.Query()
	if r.Body == nil || r.ContentLength == 0 {
		return values, nil
	}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	body := http.MaxBytesReader(nil, r.Body, maxFormBody)
	switch mt {
	case "application/json":
		var obj map[string]any
		if err := json.NewDecoder(body).Decode(&obj); err != nil {
			return nil, fmt.Errorf("%w: bad JSON body", ErrInvalid)
		}
		for k, v := range obj {
			switch v := v.(type) {
			case string:
				values.Set(k, v)
			case bool:
				values.Set(k, strconv.FormatBool(v))
			case float64:
				values.Set(k, strconv.FormatFloat(v, 'f', -1, 64))
			}
		}
	case "application/x-www-form-urlencoded", "":
		raw, err := io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("%w: body too large", ErrInvalid)
		}
		form, err := url.ParseQuery(string(raw))
		if err != nil {
			return nil, fmt.Errorf("%w: bad form body", ErrInvalid)
		}
		for k, vs := range form {
			values[k] = vs
		}
	default:
		raw, err := io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("%w: body too large", ErrInvalid)
		}
		values.Set("body", string(raw))
	}
	return values, nil
}

func truthy(s string) bool {
	switch strings.ToLower(s) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func requireUser(w http.ResponseWriter, p *Principal) bool {
	if p.Kind != "user" {
		writeError(w, http.StatusForbidden, "agents cannot do this")
		return false
	}
	return true
}

func requireAdmin(w http.ResponseWriter, p *Principal) bool {
	if !p.IsAdmin {
		writeError(w, http.StatusForbidden, "admin only")
		return false
	}
	return true
}

// Session (web UI)

func (srv *Server) login(w http.ResponseWriter, r *http.Request) {
	// Without this a cross-site form could sign the victim into the
	// attacker's account.
	if r.Header.Get(csrfHeader) != "1" {
		writeError(w, http.StatusForbidden, "missing "+csrfHeader+" header")
		return
	}
	ip := srv.clientIP(r)
	if srv.limiter.blocked(ip) {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}
	v, err := params(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var p *Principal
	token := strings.TrimSpace(v.Get("token"))
	if v.Has("password") {
		p, token, err = srv.passwordLogin(v.Get("user"), v.Get("password"))
	} else {
		p, err = srv.store.PrincipalByTokenHash(hashToken(token))
	}
	if err != nil {
		srv.limiter.fail(ip)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: 30 * 24 * 3600,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, p)
}

// passwordLogin checks the admin password from SP_ADMIN_PASSWORD, makes sure
// the admin user exists, and mints a session token for the browser.
func (srv *Server) passwordLogin(user, password string) (*Principal, string, error) {
	want := srv.cfg.AdminPassword
	userOK := equalSecret(user, srv.cfg.AdminUser)
	passOK := equalSecret(password, want)
	if want == "" || !userOK || !passOK {
		return nil, "", ErrNotFound
	}
	p, err := srv.store.PrincipalByName(srv.cfg.AdminUser)
	if errors.Is(err, ErrNotFound) {
		p, err = srv.store.CreateUser(srv.cfg.AdminUser, true)
	}
	if err != nil {
		return nil, "", err
	}
	if !p.IsAdmin || p.Kind != "user" {
		return nil, "", ErrNotFound
	}
	token, _, err := srv.store.IssueToken(p, "password login")
	return p, token, err
}

// equalSecret compares in constant time without leaking the length.
func equalSecret(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

func (srv *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		srv.store.DeleteSessionToken(hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (srv *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, principalFrom(r))
}

// Users (admin)

func (srv *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, principalFrom(r)) {
		return
	}
	list, err := srv.store.ListPrincipals(0)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (srv *Server) createUser(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, principalFrom(r)) {
		return
	}
	v, err := params(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	u, err := srv.store.CreateUser(v.Get("name"), truthy(v.Get("admin")))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	token, _, err := srv.store.IssueToken(u, "created by "+principalFrom(r).Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": u, "token": token})
}

func (srv *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	me := principalFrom(r)
	if !requireAdmin(w, me) {
		return
	}
	u, err := srv.store.PrincipalByName(r.PathValue("name"))
	if err != nil || u.Kind != "user" {
		writeError(w, http.StatusNotFound, "no such user")
		return
	}
	if u.ID == me.ID {
		writeError(w, http.StatusBadRequest, "cannot delete yourself")
		return
	}
	if err := srv.store.DeletePrincipal(u.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Agents: every user owns a group of agents named <user>/<slot>.

// agentOwner resolves whose agent group the request targets: the caller, or
// any user when an admin passes owner=.
func (srv *Server) agentOwner(w http.ResponseWriter, r *http.Request, v url.Values) (*Principal, bool) {
	me := principalFrom(r)
	if !requireUser(w, me) {
		return nil, false
	}
	name := v.Get("owner")
	if name == "" || name == me.Name {
		return me, true
	}
	if !requireAdmin(w, me) {
		return nil, false
	}
	owner, err := srv.store.PrincipalByName(name)
	if err != nil || owner.Kind != "user" {
		writeError(w, http.StatusNotFound, "no such user")
		return nil, false
	}
	return owner, true
}

func (srv *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	owner, ok := srv.agentOwner(w, r, r.URL.Query())
	if !ok {
		return
	}
	list, err := srv.store.ListPrincipals(owner.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (srv *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	v, err := params(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	owner, ok := srv.agentOwner(w, r, v)
	if !ok {
		return
	}
	agent, err := srv.store.CreateAgent(owner, v.Get("slot"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	token, _, err := srv.store.IssueToken(agent, v.Get("label"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"agent": agent, "token": token})
}

func (srv *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	owner, ok := srv.agentOwner(w, r, r.URL.Query())
	if !ok {
		return
	}
	agent, err := srv.store.PrincipalByName(owner.Name + "/" + r.PathValue("slot"))
	if err != nil {
		writeError(w, http.StatusNotFound, "no such agent")
		return
	}
	if err := srv.store.DeletePrincipal(agent.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Tokens

// canManage reports whether me may manage credentials of target: itself, its
// own agents, or anyone for admins.
func canManage(me, target *Principal) bool {
	return me.IsAdmin || me.ID == target.ID || (target.Kind == "agent" && target.OwnerID == me.ID)
}

func (srv *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	list, err := srv.store.ListTokens(principalFrom(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	mine := hashToken(requestToken(r))
	for i := range list {
		list[i].Current = subtle.ConstantTimeCompare(list[i].hash, mine) == 1
	}
	writeJSON(w, http.StatusOK, list)
}

func (srv *Server) createToken(w http.ResponseWriter, r *http.Request) {
	me := principalFrom(r)
	if !requireUser(w, me) {
		return
	}
	v, err := params(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	target := me
	if name := v.Get("for"); name != "" {
		if target, err = srv.store.PrincipalByName(name); err != nil {
			writeError(w, http.StatusNotFound, "no such principal")
			return
		}
	}
	if !canManage(me, target) {
		writeError(w, http.StatusForbidden, "not yours")
		return
	}
	token, id, err := srv.store.IssueToken(target, v.Get("label"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "principal": target.Name, "token": token})
}

func (srv *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	me := principalFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad token id")
		return
	}
	owner, err := srv.store.TokenOwner(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !canManage(me, owner) {
		writeError(w, http.StatusForbidden, "not yours")
		return
	}
	if err := srv.store.DeleteToken(id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Topics

// topicAccess loads the topic in the path and checks the caller holds at
// least the wanted permission. Topics the caller cannot read look missing.
func (srv *Server) topicAccess(w http.ResponseWriter, r *http.Request, want string) (*Topic, *Principal, bool) {
	me := principalFrom(r)
	t, err := srv.store.TopicBySlug(r.PathValue("topic"))
	if err != nil {
		writeError(w, http.StatusNotFound, "no such topic")
		return nil, nil, false
	}
	perm, err := srv.store.Perm(me, t.ID)
	if err != nil {
		writeStoreError(w, err)
		return nil, nil, false
	}
	if perm == "" {
		writeError(w, http.StatusNotFound, "no such topic")
		return nil, nil, false
	}
	if permRank[perm] < permRank[want] {
		writeError(w, http.StatusForbidden, "needs "+want+" permission, you have "+perm)
		return nil, nil, false
	}
	t.Perm = perm
	return t, me, true
}

func (srv *Server) listTopics(w http.ResponseWriter, r *http.Request) {
	me := principalFrom(r)
	all, err := srv.store.AllTopics()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := []Topic{}
	for _, t := range all {
		perm, err := srv.store.Perm(me, t.ID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if perm != "" {
			t.Perm = perm
			out = append(out, t)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (srv *Server) createTopic(w http.ResponseWriter, r *http.Request) {
	me := principalFrom(r)
	if !requireUser(w, me) {
		return
	}
	v, err := params(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	t, err := srv.store.CreateTopic(v.Get("slug"), v.Get("title"), me)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	t.Perm = "admin"
	writeJSON(w, http.StatusCreated, t)
}

func (srv *Server) deleteTopic(w http.ResponseWriter, r *http.Request) {
	t, _, ok := srv.topicAccess(w, r, "admin")
	if !ok {
		return
	}
	if err := srv.store.DeleteTopic(t.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (srv *Server) listGrants(w http.ResponseWriter, r *http.Request) {
	t, _, ok := srv.topicAccess(w, r, "read")
	if !ok {
		return
	}
	grants, err := srv.store.Grants(t.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, grants)
}

func (srv *Server) setGrant(w http.ResponseWriter, r *http.Request) {
	t, _, ok := srv.topicAccess(w, r, "admin")
	if !ok {
		return
	}
	v, err := params(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	perm := v.Get("perm")
	if perm == "" {
		perm = "write"
	}
	if err := srv.store.SetGrant(t.ID, r.PathValue("subject"), perm); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Grant{Subject: r.PathValue("subject"), Perm: perm})
}

func (srv *Server) deleteGrant(w http.ResponseWriter, r *http.Request) {
	t, _, ok := srv.topicAccess(w, r, "admin")
	if !ok {
		return
	}
	if err := srv.store.DeleteGrant(t.ID, r.PathValue("subject")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Files and folders

func pathParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, err := CleanPath(r.PathValue("path"))
	if err != nil {
		writeStoreError(w, err)
		return "", false
	}
	return p, true
}

func wantsJSON(r *http.Request) bool {
	return truthy(r.URL.Query().Get("meta")) || strings.Contains(r.Header.Get("Accept"), "application/json")
}

func (srv *Server) tree(w http.ResponseWriter, r *http.Request) {
	t, _, ok := srv.topicAccess(w, r, "read")
	if !ok {
		return
	}
	nodes, err := srv.store.ListNodes(t.ID, "", true)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"topic": t, "nodes": nodes})
}

func (srv *Server) search(w http.ResponseWriter, r *http.Request) {
	t, _, ok := srv.topicAccess(w, r, "read")
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, "missing q")
		return
	}
	hits, err := srv.store.Search(t.ID, q)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hits)
}

func (srv *Server) readFile(w http.ResponseWriter, r *http.Request) {
	t, _, ok := srv.topicAccess(w, r, "read")
	if !ok {
		return
	}
	p, ok := pathParam(w, r)
	if !ok {
		return
	}
	if p != "" {
		n, err := srv.store.GetNode(t.ID, p)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if n.Kind == "file" {
			if revStr := r.URL.Query().Get("rev"); revStr != "" {
				rev, _ := strconv.ParseInt(revStr, 10, 64)
				content, err := srv.store.RevisionContent(n.ID, rev)
				if err != nil {
					writeStoreError(w, err)
					return
				}
				n.Content, n.Rev, n.Size = content, rev, len(content)
			}
			w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(n.Rev, 10)))
			if wantsJSON(r) {
				writeJSON(w, http.StatusOK, n)
				return
			}
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.Header().Set("X-SP-Rev", strconv.FormatInt(n.Rev, 10))
			w.Header().Set("X-SP-Updated-By", n.UpdatedBy)
			io.WriteString(w, n.Content)
			return
		}
	}
	children, err := srv.store.ListNodes(t.ID, p, truthy(r.URL.Query().Get("recursive")))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "kind": "dir", "children": children})
}

// expectedRev reads If-Match / If-None-Match: * / ?if_rev= into the revision
// the write must apply to; -1 means unconditional.
func expectedRev(r *http.Request) int64 {
	if r.Header.Get("If-None-Match") == "*" {
		return 0
	}
	raw := r.Header.Get("If-Match")
	if raw == "" {
		raw = r.URL.Query().Get("if_rev")
	}
	raw = strings.Trim(strings.TrimPrefix(raw, "W/"), `"`)
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return n
	}
	return -1
}

func readContent(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFileSize))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file larger than %d bytes", maxFileSize))
		return "", false
	}
	if !utf8.Valid(raw) {
		writeError(w, http.StatusUnsupportedMediaType, "content must be UTF-8 text")
		return "", false
	}
	return string(raw), true
}

func (srv *Server) writeFile(w http.ResponseWriter, r *http.Request) {
	srv.putFile(w, r, false)
}

func (srv *Server) appendFile(w http.ResponseWriter, r *http.Request) {
	srv.putFile(w, r, true)
}

func (srv *Server) putFile(w http.ResponseWriter, r *http.Request, appendMode bool) {
	t, me, ok := srv.topicAccess(w, r, "write")
	if !ok {
		return
	}
	p, ok := pathParam(w, r)
	if !ok {
		return
	}
	if p == "" {
		writeError(w, http.StatusBadRequest, "missing path")
		return
	}
	if !appendMode && strings.HasSuffix(r.URL.Path, "/") {
		n, err := srv.store.MakeDir(t.ID, p, me.Name)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, n)
		return
	}
	content, ok := readContent(w, r)
	if !ok {
		return
	}
	ifRev := expectedRev(r)
	n, err := srv.store.WriteFile(t.ID, p, content, me.Name, ifRev, appendMode)
	if errors.Is(err, ErrConflict) && ifRev >= 0 {
		writeError(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	n.Content = ""
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(n.Rev, 10)))
	writeJSON(w, http.StatusOK, n)
}

func (srv *Server) deleteFile(w http.ResponseWriter, r *http.Request) {
	t, _, ok := srv.topicAccess(w, r, "write")
	if !ok {
		return
	}
	p, ok := pathParam(w, r)
	if !ok || p == "" {
		if ok {
			writeError(w, http.StatusBadRequest, "missing path")
		}
		return
	}
	if err := srv.store.DeleteNode(t.ID, p, truthy(r.URL.Query().Get("recursive"))); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (srv *Server) move(w http.ResponseWriter, r *http.Request) {
	t, me, ok := srv.topicAccess(w, r, "write")
	if !ok {
		return
	}
	v, err := params(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	from, err1 := CleanPath(v.Get("from"))
	to, err2 := CleanPath(v.Get("to"))
	if err := errors.Join(err1, err2); err != nil || from == "" || to == "" {
		writeError(w, http.StatusBadRequest, "from and to must be valid paths")
		return
	}
	if err := srv.store.MoveNode(t.ID, from, to, me.Name); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"from": from, "to": to})
}

func (srv *Server) history(w http.ResponseWriter, r *http.Request) {
	t, _, ok := srv.topicAccess(w, r, "read")
	if !ok {
		return
	}
	p, ok := pathParam(w, r)
	if !ok {
		return
	}
	n, err := srv.store.GetNode(t.ID, p)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	revs, err := srv.store.Revisions(n.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, revs)
}

// Comments

const maxCommentLen = 16 << 10

var mentionRe = regexp.MustCompile(`(?:^|[^\w@/])@([a-z][a-z0-9-]{0,31}(?:/(?:\*|[a-z0-9][a-z0-9-]{0,31}))?)`)

func (srv *Server) fileNode(w http.ResponseWriter, r *http.Request, want string) (*Topic, *Principal, *Node, bool) {
	t, me, ok := srv.topicAccess(w, r, want)
	if !ok {
		return nil, nil, nil, false
	}
	p, ok := pathParam(w, r)
	if !ok {
		return nil, nil, nil, false
	}
	n, err := srv.store.GetNode(t.ID, p)
	if err != nil || n.Kind != "file" {
		writeError(w, http.StatusNotFound, "no such file")
		return nil, nil, nil, false
	}
	return t, me, n, true
}

func (srv *Server) listComments(w http.ResponseWriter, r *http.Request) {
	_, _, n, ok := srv.fileNode(w, r, "read")
	if !ok {
		return
	}
	list, err := srv.store.Comments(n.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// addComment posts a reply when parent= is given, or a new thread anchored
// on quoted text. Anyone who can read the topic may reply; only people start
// threads.
func (srv *Server) addComment(w http.ResponseWriter, r *http.Request) {
	t, me, n, ok := srv.fileNode(w, r, "read")
	if !ok {
		return
	}
	v, err := params(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	body := strings.TrimSpace(v.Get("body"))
	if body == "" || len(body) > maxCommentLen {
		writeError(w, http.StatusBadRequest, "body is required and must be under 16KB")
		return
	}
	quote := strings.TrimSpace(v.Get("quote"))
	if len(quote) > 2000 {
		quote = quote[:2000]
	}
	// A thread starts from a person highlighting text in the pad; agents
	// take part by replying.
	if v.Get("parent") == "" {
		if me.Kind != "user" {
			writeError(w, http.StatusForbidden, "agents can only reply to an existing comment (pass parent=<id>)")
			return
		}
		if quote == "" {
			writeError(w, http.StatusBadRequest, "a new comment must quote text from the pad")
			return
		}
	}
	var parentID *int64
	if raw := v.Get("parent"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad parent id")
			return
		}
		parent, err := srv.store.CommentByID(id)
		if err != nil || parent.Path != n.Path || parent.TopicID != t.ID {
			writeError(w, http.StatusNotFound, "no such parent comment on this file")
			return
		}
		if parent.ParentID != nil {
			id = *parent.ParentID
		}
		parentID = &id
		quote = ""
	}
	c, err := srv.store.AddComment(n.ID, parentID, me.Name, body, quote)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	c.Path = n.Path
	srv.notifyComment(t, me, c)
	writeJSON(w, http.StatusCreated, c)
}

// notifyComment delivers inbox items to mentioned principals and to everyone
// else already in the thread. Only principals who can read the topic hear
// about it.
func (srv *Server) notifyComment(t *Topic, actor *Principal, c *Comment) {
	recipients := map[int64]string{}
	add := func(p *Principal, kind string) {
		if p == nil || p.ID == actor.ID {
			return
		}
		if perm, err := srv.store.Perm(p, t.ID); err != nil || perm == "" {
			return
		}
		if _, seen := recipients[p.ID]; !seen || kind == "mention" {
			recipients[p.ID] = kind
		}
	}
	if c.ParentID != nil {
		authors, _ := srv.store.ThreadAuthors(*c.ParentID)
		for _, name := range authors {
			p, _ := srv.store.PrincipalByName(name)
			add(p, "reply")
		}
	}
	for _, m := range mentionRe.FindAllStringSubmatch(c.Body, 20) {
		name := m[1]
		if owner, ok := strings.CutSuffix(name, "/*"); ok {
			u, err := srv.store.PrincipalByName(owner)
			if err != nil {
				continue
			}
			agents, _ := srv.store.ListPrincipals(u.ID)
			for i := range agents {
				add(&agents[i], "mention")
			}
			continue
		}
		p, _ := srv.store.PrincipalByName(name)
		add(p, "mention")
	}
	excerpt := c.Body
	if len(excerpt) > 280 {
		excerpt = strings.ToValidUTF8(excerpt[:280], "") + "…"
	}
	for id, kind := range recipients {
		cid := c.ID
		err := srv.store.AddInbox(id, InboxItem{Kind: kind, Topic: t.Slug, Path: c.Path, CommentID: &cid, Actor: actor.Name, Excerpt: excerpt})
		if err == nil {
			srv.hub.notify(id)
		}
	}
}

func (srv *Server) commentAccess(w http.ResponseWriter, r *http.Request) (*Comment, *Principal, string, bool) {
	me := principalFrom(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad comment id")
		return nil, nil, "", false
	}
	c, err := srv.store.CommentByID(id)
	if err != nil {
		writeStoreError(w, err)
		return nil, nil, "", false
	}
	perm, err := srv.store.Perm(me, c.TopicID)
	if err != nil || perm == "" {
		writeError(w, http.StatusNotFound, "not found")
		return nil, nil, "", false
	}
	return c, me, perm, true
}

func (srv *Server) resolveComment(resolved bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, me, perm, ok := srv.commentAccess(w, r)
		if !ok {
			return
		}
		if c.Author != me.Name && permRank[perm] < permRank["write"] {
			writeError(w, http.StatusForbidden, "needs write permission")
			return
		}
		if err := srv.store.SetResolved(c.ID, resolved); err != nil {
			writeStoreError(w, err)
			return
		}
		c.Resolved = resolved
		writeJSON(w, http.StatusOK, c)
	}
}

func (srv *Server) deleteComment(w http.ResponseWriter, r *http.Request) {
	c, me, perm, ok := srv.commentAccess(w, r)
	if !ok {
		return
	}
	if c.Author != me.Name && perm != "admin" {
		writeError(w, http.StatusForbidden, "only the author or a topic admin can delete")
		return
	}
	if err := srv.store.DeleteComment(c.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Inbox

const maxWait = 55 * time.Second

// inbox lists unread notifications. With wait=N it long-polls up to N
// seconds for something new, which is how agents listen without a webhook.
func (srv *Server) inbox(w http.ResponseWriter, r *http.Request) {
	me := principalFrom(r)
	q := r.URL.Query()
	all := truthy(q.Get("all"))
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	wait, _ := strconv.Atoi(q.Get("wait"))
	deadline := time.Now().Add(min(time.Duration(wait)*time.Second, maxWait))
	for {
		ch := srv.hub.wait(me.ID)
		items, err := srv.store.Inbox(me.ID, all, after)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		remaining := time.Until(deadline)
		if len(items) > 0 || remaining <= 0 {
			writeJSON(w, http.StatusOK, items)
			return
		}
		select {
		case <-ch:
		case <-time.After(remaining):
		case <-r.Context().Done():
			return
		}
	}
}

func (srv *Server) markRead(w http.ResponseWriter, r *http.Request) {
	me := principalFrom(r)
	v, err := params(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var n int64
	if id, perr := strconv.ParseInt(v.Get("id"), 10, 64); perr == nil {
		n, err = srv.store.MarkOneRead(me.ID, id)
	} else {
		upto, perr := strconv.ParseInt(v.Get("upto"), 10, 64)
		if perr != nil {
			upto = 1<<62 - 1
		}
		n, err = srv.store.MarkRead(me.ID, upto)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"marked": n})
}
