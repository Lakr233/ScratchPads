package app

import (
	"database/sql"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

// Data model
//
//	principal  user "alice" or agent "alice/1" (agents always belong to a user)
//	topic      a space like an Outline collection, addressed by slug
//	grant      topic + subject ("alice", "alice/1" or "alice/*") + perm
//	node       a folder or markdown file inside a topic, addressed by path
//	comment    a thread entry on a file, optionally quoting a passage
//	inbox      per-principal notifications (mentions, replies)

const schema = `
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS principals (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL UNIQUE,
	kind       TEXT NOT NULL CHECK (kind IN ('user', 'agent')),
	owner_id   INTEGER REFERENCES principals(id) ON DELETE CASCADE,
	is_admin   INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS tokens (
	id           INTEGER PRIMARY KEY,
	hash         BLOB NOT NULL UNIQUE,
	principal_id INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
	label        TEXT NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL,
	last_used_at INTEGER
);
CREATE TABLE IF NOT EXISTS topics (
	id         INTEGER PRIMARY KEY,
	slug       TEXT NOT NULL UNIQUE,
	title      TEXT NOT NULL,
	created_by TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS grants (
	topic_id INTEGER NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
	subject  TEXT NOT NULL,
	perm     TEXT NOT NULL CHECK (perm IN ('read', 'write', 'admin')),
	PRIMARY KEY (topic_id, subject)
);
CREATE TABLE IF NOT EXISTS nodes (
	id         INTEGER PRIMARY KEY,
	topic_id   INTEGER NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
	path       TEXT NOT NULL,
	kind       TEXT NOT NULL CHECK (kind IN ('dir', 'file')),
	content    TEXT NOT NULL DEFAULT '',
	rev        INTEGER NOT NULL DEFAULT 0,
	updated_by TEXT NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (topic_id, path)
);
CREATE TABLE IF NOT EXISTS revisions (
	node_id    INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
	rev        INTEGER NOT NULL,
	content    TEXT NOT NULL,
	author     TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (node_id, rev)
);
CREATE TABLE IF NOT EXISTS comments (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	node_id    INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
	parent_id  INTEGER REFERENCES comments(id) ON DELETE CASCADE,
	author     TEXT NOT NULL,
	body       TEXT NOT NULL,
	quote      TEXT NOT NULL DEFAULT '',
	resolved   INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS comments_node ON comments(node_id);
CREATE TABLE IF NOT EXISTS inbox (
	id           INTEGER PRIMARY KEY,
	principal_id INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
	kind         TEXT NOT NULL,
	topic        TEXT NOT NULL,
	path         TEXT NOT NULL,
	comment_id   INTEGER,
	actor        TEXT NOT NULL,
	excerpt      TEXT NOT NULL,
	created_at   INTEGER NOT NULL,
	read         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS inbox_unread ON inbox(principal_id, read, id);
`

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrInvalid  = errors.New("invalid")
)

var (
	userNameRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	agentSlotRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	topicSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

const (
	maxPathLen  = 512
	maxDepth    = 16
	maxFileSize = 2 << 20
)

type Store struct {
	db *sql.DB
}

func OpenStore(file string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", file)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := migrateCommentIDs(db); err != nil {
		return nil, fmt.Errorf("migrate comments: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrateCommentIDs rebuilds a comments table created before ids were
// AUTOINCREMENT, so a deleted comment's id is never handed out again (inbox
// items refer to comments by id).
func migrateCommentIDs(db *sql.DB) error {
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'comments'`).Scan(&ddl); err != nil {
		return err
	}
	if strings.Contains(ddl, "AUTOINCREMENT") {
		return nil
	}
	_, err := db.Exec(`PRAGMA foreign_keys = OFF;
BEGIN;
CREATE TABLE comments_new (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	node_id    INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
	parent_id  INTEGER REFERENCES comments(id) ON DELETE CASCADE,
	author     TEXT NOT NULL,
	body       TEXT NOT NULL,
	quote      TEXT NOT NULL DEFAULT '',
	resolved   INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
INSERT INTO comments_new SELECT id, node_id, parent_id, author, body, quote, resolved, created_at FROM comments;
DROP TABLE comments;
ALTER TABLE comments_new RENAME TO comments;
CREATE INDEX IF NOT EXISTS comments_node ON comments(node_id);
COMMIT;
PRAGMA foreign_keys = ON;`)
	return err
}

func now() int64 { return time.Now().Unix() }

// CleanPath normalizes a node path and rejects anything that could be
// ambiguous: empty segments, dot segments, control characters, backslashes.
func CleanPath(p string) (string, error) {
	p = strings.Trim(p, "/")
	if p == "" {
		return "", nil
	}
	if len(p) > maxPathLen {
		return "", fmt.Errorf("%w: path too long", ErrInvalid)
	}
	segments := strings.Split(p, "/")
	if len(segments) > maxDepth {
		return "", fmt.Errorf("%w: path too deep", ErrInvalid)
	}
	for _, seg := range segments {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("%w: bad path segment", ErrInvalid)
		}
		for _, r := range seg {
			if r < 0x20 || r == 0x7f || r == '\\' {
				return "", fmt.Errorf("%w: bad character in path", ErrInvalid)
			}
		}
	}
	return p, nil
}

func parentDirs(p string) []string {
	var dirs []string
	for dir := path.Dir(p); dir != "." && dir != "/"; dir = path.Dir(dir) {
		dirs = append([]string{dir}, dirs...)
	}
	return dirs
}

// Principals

type Principal struct {
	ID      int64  `json:"-"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Owner   string `json:"owner,omitempty"`
	IsAdmin bool   `json:"admin"`
	OwnerID int64  `json:"-"`
}

func (s *Store) scanPrincipal(row *sql.Row) (*Principal, error) {
	var p Principal
	var ownerID sql.NullInt64
	var ownerName sql.NullString
	err := row.Scan(&p.ID, &p.Name, &p.Kind, &ownerID, &ownerName, &p.IsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.OwnerID = ownerID.Int64
	p.Owner = ownerName.String
	return &p, nil
}

const principalCols = `p.id, p.name, p.kind, p.owner_id, o.name, p.is_admin
	FROM principals p LEFT JOIN principals o ON o.id = p.owner_id`

func (s *Store) PrincipalByName(name string) (*Principal, error) {
	return s.scanPrincipal(s.db.QueryRow(`SELECT `+principalCols+` WHERE p.name = ?`, name))
}

func (s *Store) PrincipalByID(id int64) (*Principal, error) {
	return s.scanPrincipal(s.db.QueryRow(`SELECT `+principalCols+` WHERE p.id = ?`, id))
}

func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM principals WHERE kind = 'user'`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(name string, admin bool) (*Principal, error) {
	if !userNameRe.MatchString(name) {
		return nil, fmt.Errorf("%w: user name must match %s", ErrInvalid, userNameRe)
	}
	_, err := s.db.Exec(`INSERT INTO principals (name, kind, is_admin, created_at) VALUES (?, 'user', ?, ?)`, name, admin, now())
	if err != nil {
		return nil, uniqueErr(err)
	}
	return s.PrincipalByName(name)
}

func (s *Store) CreateAgent(owner *Principal, slot string) (*Principal, error) {
	if owner.Kind != "user" {
		return nil, fmt.Errorf("%w: agents belong to users", ErrInvalid)
	}
	if !agentSlotRe.MatchString(slot) {
		return nil, fmt.Errorf("%w: agent slot must match %s", ErrInvalid, agentSlotRe)
	}
	name := owner.Name + "/" + slot
	_, err := s.db.Exec(`INSERT INTO principals (name, kind, owner_id, created_at) VALUES (?, 'agent', ?, ?)`, name, owner.ID, now())
	if err != nil {
		return nil, uniqueErr(err)
	}
	return s.PrincipalByName(name)
}

// DeletePrincipal removes a user or agent together with grants naming it, so
// a principal later created with the same name does not inherit access.
func (s *Store) DeletePrincipal(id int64) error {
	p, err := s.PrincipalByID(id)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM grants WHERE subject = ? OR (? = 'user' AND substr(subject, 1, ?) = ?)`,
		p.Name, p.Kind, runeLen(p.Name+"/"), p.Name+"/"); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM principals WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListPrincipals(ownerID int64) ([]Principal, error) {
	q := `SELECT p.id, p.name, p.kind, p.owner_id, o.name, p.is_admin
		FROM principals p LEFT JOIN principals o ON o.id = p.owner_id`
	var args []any
	if ownerID != 0 {
		q += ` WHERE p.owner_id = ?`
		args = append(args, ownerID)
	}
	q += ` ORDER BY p.name`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Principal{}
	for rows.Next() {
		var p Principal
		var ownerIDv sql.NullInt64
		var ownerName sql.NullString
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &ownerIDv, &ownerName, &p.IsAdmin); err != nil {
			return nil, err
		}
		p.OwnerID, p.Owner = ownerIDv.Int64, ownerName.String
		out = append(out, p)
	}
	return out, rows.Err()
}

// Tokens

type TokenInfo struct {
	ID         int64 `json:"id"`
	Current    bool  `json:"current,omitempty"`
	hash       []byte
	Principal  string `json:"principal"`
	Label      string `json:"label"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt *int64 `json:"last_used_at"`
}

func (s *Store) InsertToken(hash []byte, principalID int64, label string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO tokens (hash, principal_id, label, created_at) VALUES (?, ?, ?, ?)`, hash, principalID, label, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) PrincipalByTokenHash(hash []byte) (*Principal, error) {
	var id int64
	err := s.db.QueryRow(`SELECT principal_id FROM tokens WHERE hash = ?`, hash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_, _ = s.db.Exec(`UPDATE tokens SET last_used_at = ? WHERE hash = ? AND (last_used_at IS NULL OR last_used_at < ?)`, now(), hash, now()-60)
	return s.PrincipalByID(id)
}

// ListTokens returns tokens of the principal and, for users, of their agents.
func (s *Store) ListTokens(p *Principal) ([]TokenInfo, error) {
	rows, err := s.db.Query(`SELECT t.id, t.hash, pr.name, t.label, t.created_at, t.last_used_at
		FROM tokens t JOIN principals pr ON pr.id = t.principal_id
		WHERE pr.id = ? OR pr.owner_id = ? ORDER BY t.id`, p.ID, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TokenInfo{}
	for rows.Next() {
		var t TokenInfo
		var last sql.NullInt64
		if err := rows.Scan(&t.ID, &t.hash, &t.Principal, &t.Label, &t.CreatedAt, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			t.LastUsedAt = &last.Int64
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) TokenOwner(id int64) (*Principal, error) {
	var pid int64
	err := s.db.QueryRow(`SELECT principal_id FROM tokens WHERE id = ?`, id).Scan(&pid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.PrincipalByID(pid)
}

func (s *Store) DeleteToken(id int64) error {
	_, err := s.db.Exec(`DELETE FROM tokens WHERE id = ?`, id)
	return err
}

// DeleteSessionToken revokes a token minted by password login. Tokens a user
// pasted into the login form are left alone, since agents may share them.
func (s *Store) DeleteSessionToken(hash []byte) error {
	_, err := s.db.Exec(`DELETE FROM tokens WHERE hash = ? AND label = 'password login'`, hash)
	return err
}

// Topics and grants

type Topic struct {
	ID        int64  `json:"-"`
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	CreatedBy string `json:"created_by"`
	CreatedAt int64  `json:"created_at"`
	Perm      string `json:"perm,omitempty"`
}

func (s *Store) TopicBySlug(slug string) (*Topic, error) {
	var t Topic
	err := s.db.QueryRow(`SELECT id, slug, title, created_by, created_at FROM topics WHERE slug = ?`, slug).
		Scan(&t.ID, &t.Slug, &t.Title, &t.CreatedBy, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &t, err
}

func (s *Store) CreateTopic(slug, title string, creator *Principal) (*Topic, error) {
	if !topicSlugRe.MatchString(slug) {
		return nil, fmt.Errorf("%w: topic slug must match %s", ErrInvalid, topicSlugRe)
	}
	if title == "" {
		title = slug
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO topics (slug, title, created_by, created_at) VALUES (?, ?, ?, ?)`, slug, title, creator.Name, now())
	if err != nil {
		return nil, uniqueErr(err)
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO grants (topic_id, subject, perm) VALUES (?, ?, 'admin')`, id, creator.Name); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.TopicBySlug(slug)
}

// DeleteTopic removes the topic and inbox items pointing into it.
func (s *Store) DeleteTopic(id int64) error {
	_, err := s.db.Exec(`DELETE FROM inbox WHERE topic = (SELECT slug FROM topics WHERE id = ?);
		DELETE FROM topics WHERE id = ?`, id, id)
	return err
}

func (s *Store) AllTopics() ([]Topic, error) {
	rows, err := s.db.Query(`SELECT id, slug, title, created_by, created_at FROM topics ORDER BY title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Topic{}
	for rows.Next() {
		var t Topic
		if err := rows.Scan(&t.ID, &t.Slug, &t.Title, &t.CreatedBy, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Subjects lists grant subjects that apply to a principal.
func Subjects(p *Principal) []string {
	if p.Kind == "agent" {
		return []string{p.Name, p.Owner + "/*"}
	}
	return []string{p.Name}
}

var permRank = map[string]int{"": 0, "read": 1, "write": 2, "admin": 3}

func (s *Store) Perm(p *Principal, topicID int64) (string, error) {
	if p.IsAdmin {
		return "admin", nil
	}
	subjects := Subjects(p)
	rows, err := s.db.Query(`SELECT perm FROM grants WHERE topic_id = ? AND subject IN (?, ?)`, topicID, subjects[0], subjects[len(subjects)-1])
	if err != nil {
		return "", err
	}
	defer rows.Close()
	best := ""
	for rows.Next() {
		var perm string
		if err := rows.Scan(&perm); err != nil {
			return "", err
		}
		if permRank[perm] > permRank[best] {
			best = perm
		}
	}
	return best, rows.Err()
}

type Grant struct {
	Subject string `json:"subject"`
	Perm    string `json:"perm"`
}

func (s *Store) Grants(topicID int64) ([]Grant, error) {
	rows, err := s.db.Query(`SELECT subject, perm FROM grants WHERE topic_id = ? ORDER BY subject`, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.Subject, &g.Perm); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ValidSubject accepts "alice", "alice/1" and "alice/*".
func ValidSubject(subject string) bool {
	user, slot, isAgent := strings.Cut(subject, "/")
	if !userNameRe.MatchString(user) {
		return false
	}
	return !isAgent || slot == "*" || agentSlotRe.MatchString(slot)
}

func (s *Store) SetGrant(topicID int64, subject, perm string) error {
	if !ValidSubject(subject) {
		return fmt.Errorf("%w: subject must be user, user/slot or user/*", ErrInvalid)
	}
	if perm != "read" && perm != "write" && perm != "admin" {
		return fmt.Errorf("%w: perm must be read, write or admin", ErrInvalid)
	}
	_, err := s.db.Exec(`INSERT INTO grants (topic_id, subject, perm) VALUES (?, ?, ?)
		ON CONFLICT (topic_id, subject) DO UPDATE SET perm = excluded.perm`, topicID, subject, perm)
	return err
}

func (s *Store) DeleteGrant(topicID int64, subject string) error {
	_, err := s.db.Exec(`DELETE FROM grants WHERE topic_id = ? AND subject = ?`, topicID, subject)
	return err
}

// Nodes

type Node struct {
	ID        int64  `json:"-"`
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Rev       int64  `json:"rev"`
	Size      int    `json:"size"`
	UpdatedBy string `json:"updated_by"`
	UpdatedAt int64  `json:"updated_at"`
	Content   string `json:"content,omitempty"`
	Comments  int    `json:"comments,omitempty"`
}

func (s *Store) GetNode(topicID int64, p string) (*Node, error) {
	var n Node
	err := s.db.QueryRow(`SELECT id, path, kind, rev, length(content), updated_by, updated_at, content
		FROM nodes WHERE topic_id = ? AND path = ?`, topicID, p).
		Scan(&n.ID, &n.Path, &n.Kind, &n.Rev, &n.Size, &n.UpdatedBy, &n.UpdatedAt, &n.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &n, err
}

// ListNodes returns every node under prefix (all nodes when prefix is empty).
// Direct children only unless recursive is set.
func (s *Store) ListNodes(topicID int64, prefix string, recursive bool) ([]Node, error) {
	rows, err := s.db.Query(`SELECT n.id, n.path, n.kind, n.rev, length(n.content), n.updated_by, n.updated_at,
			(SELECT COUNT(*) FROM comments c WHERE c.node_id = n.id AND c.parent_id IS NULL AND c.resolved = 0)
		FROM nodes n WHERE n.topic_id = ? ORDER BY n.kind, n.path`, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Node{}
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.Path, &n.Kind, &n.Rev, &n.Size, &n.UpdatedBy, &n.UpdatedAt, &n.Comments); err != nil {
			return nil, err
		}
		rel := n.Path
		if prefix != "" {
			if !strings.HasPrefix(n.Path, prefix+"/") {
				continue
			}
			rel = strings.TrimPrefix(n.Path, prefix+"/")
		}
		if !recursive && strings.Contains(rel, "/") {
			continue
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func ensureDirs(tx *sql.Tx, topicID int64, p, actor string) error {
	for _, dir := range parentDirs(p) {
		var kind string
		err := tx.QueryRow(`SELECT kind FROM nodes WHERE topic_id = ? AND path = ?`, topicID, dir).Scan(&kind)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.Exec(`INSERT INTO nodes (topic_id, path, kind, updated_by, updated_at) VALUES (?, ?, 'dir', ?, ?)`, topicID, dir, actor, now()); err != nil {
				return err
			}
		case err != nil:
			return err
		case kind != "dir":
			return fmt.Errorf("%w: %s is a file", ErrConflict, dir)
		}
	}
	return nil
}

func (s *Store) MakeDir(topicID int64, p, actor string) (*Node, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := ensureDirs(tx, topicID, p+"/x", actor); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetNode(topicID, p)
}

// WriteFile creates or replaces (or appends to) a file. ifRev >= 0 enforces
// optimistic concurrency against the current revision (0 = must not exist).
func (s *Store) WriteFile(topicID int64, p, content, actor string, ifRev int64, appendMode bool) (*Node, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := ensureDirs(tx, topicID, p, actor); err != nil {
		return nil, err
	}
	var id, rev int64
	var kind, old string
	err = tx.QueryRow(`SELECT id, kind, rev, content FROM nodes WHERE topic_id = ? AND path = ?`, topicID, p).Scan(&id, &kind, &rev, &old)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if exists && kind == "dir" {
		return nil, fmt.Errorf("%w: %s is a folder", ErrConflict, p)
	}
	if ifRev >= 0 && ifRev != rev {
		return nil, fmt.Errorf("%w: current rev is %d", ErrConflict, rev)
	}
	if appendMode {
		content = old + content
	}
	if len(content) > maxFileSize {
		return nil, fmt.Errorf("%w: file larger than %d bytes", ErrInvalid, maxFileSize)
	}
	rev++
	if exists {
		_, err = tx.Exec(`UPDATE nodes SET content = ?, rev = ?, updated_by = ?, updated_at = ? WHERE id = ?`, content, rev, actor, now(), id)
	} else {
		var res sql.Result
		res, err = tx.Exec(`INSERT INTO nodes (topic_id, path, kind, content, rev, updated_by, updated_at) VALUES (?, ?, 'file', ?, ?, ?, ?)`, topicID, p, content, rev, actor, now())
		if err == nil {
			id, _ = res.LastInsertId()
		}
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO revisions (node_id, rev, content, author, created_at) VALUES (?, ?, ?, ?, ?)`, id, rev, content, actor, now()); err != nil {
		return nil, err
	}
	// Keep the last 50 revisions per file.
	if _, err := tx.Exec(`DELETE FROM revisions WHERE node_id = ? AND rev <= ?`, id, rev-50); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetNode(topicID, p)
}

func (s *Store) DeleteNode(topicID int64, p string, recursive bool) error {
	n, err := s.GetNode(topicID, p)
	if err != nil {
		return err
	}
	if n.Kind == "dir" {
		children, err := s.ListNodes(topicID, p, false)
		if err != nil {
			return err
		}
		if len(children) > 0 && !recursive {
			return fmt.Errorf("%w: folder not empty, pass recursive=1", ErrConflict)
		}
	}
	_, err = s.db.Exec(`DELETE FROM nodes WHERE topic_id = ? AND (path = ? OR substr(path, 1, ?) = ?)`, topicID, p, runeLen(p+"/"), p+"/")
	return err
}

func (s *Store) MoveNode(topicID int64, from, to, actor string) error {
	if from == to || strings.HasPrefix(to, from+"/") {
		return fmt.Errorf("%w: cannot move into itself", ErrInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM nodes WHERE topic_id = ? AND path = ?`, topicID, from).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM nodes WHERE topic_id = ? AND path = ?`, topicID, to).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: %s already exists", ErrConflict, to)
	}
	if err := ensureDirs(tx, topicID, to, actor); err != nil {
		return err
	}
	// Prefix match with substr, not LIKE: LIKE is case-insensitive, and
	// substr counts characters, so lengths are in runes.
	_, err = tx.Exec(`UPDATE nodes SET path = ? || substr(path, ?), updated_by = ?, updated_at = ?
		WHERE topic_id = ? AND (path = ? OR substr(path, 1, ?) = ?)`,
		to, runeLen(from)+1, actor, now(), topicID, from, runeLen(from+"/"), from+"/")
	if err != nil {
		return uniqueErr(err)
	}
	return tx.Commit()
}

type Revision struct {
	Rev       int64  `json:"rev"`
	Author    string `json:"author"`
	CreatedAt int64  `json:"created_at"`
	Size      int    `json:"size"`
}

func (s *Store) Revisions(nodeID int64) ([]Revision, error) {
	rows, err := s.db.Query(`SELECT rev, author, created_at, length(content) FROM revisions WHERE node_id = ? ORDER BY rev DESC`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		var r Revision
		if err := rows.Scan(&r.Rev, &r.Author, &r.CreatedAt, &r.Size); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) RevisionContent(nodeID, rev int64) (string, error) {
	var c string
	err := s.db.QueryRow(`SELECT content FROM revisions WHERE node_id = ? AND rev = ?`, nodeID, rev).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return c, err
}

type SearchHit struct {
	Path    string `json:"path"`
	Snippet string `json:"snippet"`
}

func (s *Store) Search(topicID int64, q string) ([]SearchHit, error) {
	pattern := "%" + likeEscape(q) + "%"
	rows, err := s.db.Query(`SELECT path, content FROM nodes WHERE topic_id = ? AND kind = 'file'
		AND (content LIKE ? ESCAPE '\' OR path LIKE ? ESCAPE '\') ORDER BY updated_at DESC LIMIT 50`, topicID, pattern, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SearchHit{}
	lq := strings.ToLower(q)
	for rows.Next() {
		var path, content string
		if err := rows.Scan(&path, &content); err != nil {
			return nil, err
		}
		out = append(out, SearchHit{Path: path, Snippet: snippet(content, lq)})
	}
	return out, rows.Err()
}

func snippet(content, lq string) string {
	// Lowercasing can change byte lengths, so clamp the index to content.
	i := strings.Index(strings.ToLower(content), lq)
	if i < 0 || i > len(content) {
		i = 0
	}
	end := min(len(content), i+len(lq)+60)
	start := min(max(0, i-60), end)
	for start > 0 && !utf8Start(content[start]) {
		start--
	}
	for end < len(content) && !utf8Start(content[end]) {
		end++
	}
	return strings.TrimSpace(content[start:end])
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// Comments

type Comment struct {
	ID        int64  `json:"id"`
	ParentID  *int64 `json:"parent_id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	Quote     string `json:"quote,omitempty"`
	Resolved  bool   `json:"resolved"`
	CreatedAt int64  `json:"created_at"`
	Path      string `json:"path,omitempty"`
	TopicID   int64  `json:"-"`
}

func (s *Store) Comments(nodeID int64) ([]Comment, error) {
	rows, err := s.db.Query(`SELECT id, parent_id, author, body, quote, resolved, created_at
		FROM comments WHERE node_id = ? ORDER BY id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		var c Comment
		var parent sql.NullInt64
		if err := rows.Scan(&c.ID, &parent, &c.Author, &c.Body, &c.Quote, &c.Resolved, &c.CreatedAt); err != nil {
			return nil, err
		}
		if parent.Valid {
			c.ParentID = &parent.Int64
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CommentByID(id int64) (*Comment, error) {
	var c Comment
	var parent sql.NullInt64
	err := s.db.QueryRow(`SELECT c.id, c.parent_id, c.author, c.body, c.quote, c.resolved, c.created_at, n.path, n.topic_id
		FROM comments c JOIN nodes n ON n.id = c.node_id WHERE c.id = ?`, id).
		Scan(&c.ID, &parent, &c.Author, &c.Body, &c.Quote, &c.Resolved, &c.CreatedAt, &c.Path, &c.TopicID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if parent.Valid {
		c.ParentID = &parent.Int64
	}
	return &c, err
}

// ThreadAuthors returns everyone who wrote in the thread rooted at rootID.
func (s *Store) ThreadAuthors(rootID int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT author FROM comments WHERE id = ? OR parent_id = ?`, rootID, rootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) AddComment(nodeID int64, parentID *int64, author, body, quote string) (*Comment, error) {
	res, err := s.db.Exec(`INSERT INTO comments (node_id, parent_id, author, body, quote, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		nodeID, parentID, author, body, quote, now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.CommentByID(id)
}

func (s *Store) SetResolved(id int64, resolved bool) error {
	_, err := s.db.Exec(`UPDATE comments SET resolved = ? WHERE id = ?`, resolved, id)
	return err
}

func (s *Store) DeleteComment(id int64) error {
	_, err := s.db.Exec(`DELETE FROM comments WHERE id = ?`, id)
	return err
}

// Inbox

type InboxItem struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Topic     string `json:"topic"`
	Path      string `json:"path"`
	CommentID *int64 `json:"comment_id,omitempty"`
	Actor     string `json:"actor"`
	Excerpt   string `json:"excerpt"`
	CreatedAt int64  `json:"created_at"`
	Read      bool   `json:"read"`
}

func (s *Store) AddInbox(principalID int64, item InboxItem) error {
	_, err := s.db.Exec(`INSERT INTO inbox (principal_id, kind, topic, path, comment_id, actor, excerpt, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		principalID, item.Kind, item.Topic, item.Path, item.CommentID, item.Actor, item.Excerpt, now())
	return err
}

func (s *Store) Inbox(principalID int64, includeRead bool, after int64) ([]InboxItem, error) {
	q := `SELECT id, kind, topic, path, comment_id, actor, excerpt, created_at, read FROM inbox WHERE principal_id = ? AND id > ?`
	if !includeRead {
		q += ` AND read = 0`
	}
	q += ` ORDER BY id DESC LIMIT 100`
	rows, err := s.db.Query(q, principalID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InboxItem{}
	for rows.Next() {
		var it InboxItem
		var cid sql.NullInt64
		if err := rows.Scan(&it.ID, &it.Kind, &it.Topic, &it.Path, &cid, &it.Actor, &it.Excerpt, &it.CreatedAt, &it.Read); err != nil {
			return nil, err
		}
		if cid.Valid {
			it.CommentID = &cid.Int64
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) MarkOneRead(principalID, id int64) (int64, error) {
	res, err := s.db.Exec(`UPDATE inbox SET read = 1 WHERE principal_id = ? AND id = ?`, principalID, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) MarkRead(principalID, upto int64) (int64, error) {
	res, err := s.db.Exec(`UPDATE inbox SET read = 1 WHERE principal_id = ? AND id <= ? AND read = 0`, principalID, upto)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func uniqueErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return fmt.Errorf("%w: already exists", ErrConflict)
	}
	return err
}
