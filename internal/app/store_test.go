package app

import (
	"errors"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func paths(t *testing.T, s *Store, topicID int64) map[string]bool {
	t.Helper()
	nodes, err := s.ListNodes(topicID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, n := range nodes {
		out[n.Path] = true
	}
	return out
}

func TestCleanPath(t *testing.T) {
	for in, want := range map[string]string{"/a/b.md/": "a/b.md", "": "", "中文/页.md": "中文/页.md"} {
		if got, err := CleanPath(in); err != nil || got != want {
			t.Errorf("CleanPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"a/../b", "a//b", "./a", "a\\b", "a/\x00"} {
		if _, err := CleanPath(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("CleanPath(%q) accepted", bad)
		}
	}
}

func TestDeleteAndMoveArePrefixExact(t *testing.T) {
	s := newTestStore(t)
	u, _ := s.CreateUser("alice", false)
	topic, _ := s.CreateTopic("t", "T", u)
	for _, p := range []string{"a/x.md", "A/y.md", "ab/z.md", "é/a.md"} {
		if _, err := s.WriteFile(topic.ID, p, "x", "alice", -1, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteNode(topic.ID, "a", true); err != nil {
		t.Fatal(err)
	}
	got := paths(t, s, topic.ID)
	if got["a"] || got["a/x.md"] || !got["A/y.md"] || !got["ab/z.md"] {
		t.Fatalf("delete a touched the wrong nodes: %v", got)
	}
	if err := s.MoveNode(topic.ID, "é", "x", "alice"); err != nil {
		t.Fatal(err)
	}
	if got := paths(t, s, topic.ID); !got["x/a.md"] || got["é/a.md"] {
		t.Fatalf("move of non-ASCII folder broke children: %v", got)
	}
}

func TestPermissionsAndGroupGrants(t *testing.T) {
	s := newTestStore(t)
	alice, _ := s.CreateUser("alice", false)
	bob, _ := s.CreateUser("bob", false)
	agent, _ := s.CreateAgent(alice, "1")
	topic, _ := s.CreateTopic("t", "T", alice)

	check := func(p *Principal, want string) {
		t.Helper()
		if got, _ := s.Perm(p, topic.ID); got != want {
			t.Errorf("Perm(%s) = %q, want %q", p.Name, got, want)
		}
	}
	check(alice, "admin")
	check(bob, "")
	check(agent, "")
	if err := s.SetGrant(topic.ID, "alice/*", "write"); err != nil {
		t.Fatal(err)
	}
	check(agent, "write")

	// Deleting a principal drops its grants, so a re-created name starts clean.
	if err := s.SetGrant(topic.ID, "bob", "read"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePrincipal(bob.ID); err != nil {
		t.Fatal(err)
	}
	bob2, _ := s.CreateUser("bob", false)
	check(bob2, "")
}

func TestWriteFileConcurrency(t *testing.T) {
	s := newTestStore(t)
	u, _ := s.CreateUser("alice", false)
	topic, _ := s.CreateTopic("t", "T", u)
	n, err := s.WriteFile(topic.ID, "p.md", "one", "alice", 0, false)
	if err != nil || n.Rev != 1 {
		t.Fatalf("create: %v rev %d", err, n.Rev)
	}
	if _, err := s.WriteFile(topic.ID, "p.md", "two", "alice", 0, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("If-None-Match on existing file: %v", err)
	}
	if _, err := s.WriteFile(topic.ID, "p.md", "two", "alice", 5, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale If-Match: %v", err)
	}
	n, err = s.WriteFile(topic.ID, "p.md", "+", "alice", 1, true)
	if err != nil || n.Content != "one+" || n.Rev != 2 {
		t.Fatalf("append: %v %q rev %d", err, n.Content, n.Rev)
	}
}

func TestSnippetNeverPanics(t *testing.T) {
	// U+023A grows when lowercased, which used to push the index past the end.
	content := "ȺȺȺȺȺȺȺȺȺȺ needle"
	if got := snippet(content, "needle"); got == "" {
		t.Fatal("empty snippet")
	}
}
