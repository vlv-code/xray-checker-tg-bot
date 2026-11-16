package nodes

import (
	"strings"
	"testing"
)

func TestParseNodes(t *testing.T) {
	got, err := ParseNodes([]string{"node-1|secrettoken1", "node-2|secrettoken2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(got))
	}
	if got[0].Name != "node-1" || got[0].Token != "secrettoken1" {
		t.Errorf("bad first entry: %+v", got[0])
	}
}

func TestParseNodesErrors(t *testing.T) {
	cases := [][]string{
		{"noname"},       // missing token
		{"|token"},       // missing name
		{"a|t1", "a|t2"}, // duplicate name
		{"a|t1|extra"},   // too many parts
		{""},             // empty entry
	}
	for _, c := range cases {
		if _, err := ParseNodes(c); err == nil {
			t.Errorf("ParseNodes(%q) should fail", c)
		}
	}
}

func TestParseNodesErrorsDoNotLeakTokens(t *testing.T) {
	t.Run("wrong arity hides token", func(t *testing.T) {
		_, err := ParseNodes([]string{"ok|tok1", "x|SECRETTOKEN|extra"})
		if err == nil {
			t.Fatal("expected error for entry with extra parts, got nil")
		}
		msg := err.Error()
		if strings.Contains(msg, "SECRETTOKEN") {
			t.Fatalf("arity error leaks the bad entry's token: %s", msg)
		}
		if !strings.Contains(msg, "entry 2") {
			t.Errorf("arity error must include entry index: %s", msg)
		}
		if !strings.Contains(msg, "\"x\"") {
			t.Errorf("arity error must mention the name portion: %s", msg)
		}
	})

	t.Run("empty name hides token", func(t *testing.T) {
		_, err := ParseNodes([]string{"ok|tok1", " |SECRETTOKEN"})
		if err == nil {
			t.Fatal("expected error for entry with empty name, got nil")
		}
		msg := err.Error()
		if strings.Contains(msg, "SECRETTOKEN") {
			t.Fatalf("empty-name error leaks the bad entry's token: %s", msg)
		}
		if !strings.Contains(msg, "entry 2") {
			t.Errorf("empty-name error must include entry index: %s", msg)
		}
	})

	t.Run("missing pipe mentions name only", func(t *testing.T) {
		// NODES env "ok|tok1,name-only": the name-only entry has no token to
		// leak, but the error must not echo sibling tokens either.
		_, err := ParseNodes([]string{"ok|tok1", "name-only"})
		if err == nil {
			t.Fatal("expected error for entry missing '|', got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, "name-only") {
			t.Errorf("error must mention the name-only entry: %s", msg)
		}
		if strings.Contains(msg, "tok1") {
			t.Errorf("error leaks a sibling entry's token: %s", msg)
		}
	})
}
