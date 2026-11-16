package nodes

import "testing"

// TestParseNodesSkipsReservedNames (Wave-F B6): node names colliding with
// static Telegram diag routes ("local", "rich", "summary", "details") must be
// skipped with a warning — a static NODES env entry must not hard-fail the
// whole master startup — while regular entries parse as before.
func TestParseNodesSkipsReservedNames(t *testing.T) {
	got, err := ParseNodes([]string{"local|tok1", "ok-node|tok2", "details|tok3"})
	if err != nil {
		t.Fatalf("reserved entries must be skipped without error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "ok-node" || got[0].Token != "tok2" {
		t.Fatalf("expected only ok-node to survive reserved-name filtering, got %+v", got)
	}

	// Control: no reserved names → unchanged behavior.
	ctl, err := ParseNodes([]string{"a|t1", "b|t2"})
	if err != nil || len(ctl) != 2 {
		t.Fatalf("control parse must stay intact, got %+v err %v", ctl, err)
	}
}
