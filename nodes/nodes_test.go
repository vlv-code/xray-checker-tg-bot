package nodes

import "testing"

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
