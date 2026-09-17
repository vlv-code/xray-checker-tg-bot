package main

import (
	"path/filepath"
	"testing"

	"xray-checker/nodes"
)

func TestNodeManagerAdapter(t *testing.T) {
	reg := nodes.NewRegistry([]nodes.NodeConfig{{Name: "n1", Token: "t"}}, nil, nil)
	subs, err := nodes.NewNodeSubsStore(filepath.Join(t.TempDir(), "subs.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &nodeManagerAdapter{reg: reg, subs: subs}

	if err := a.AddSub("nope", "https://x/y"); err == nil {
		t.Error("unknown node must error")
	}
	if err := a.AddSub("n1", "https://x/y"); err != nil {
		t.Fatal(err)
	}
	if err := a.AddSub("n1", "https://x/y"); err == nil {
		t.Error("duplicate must error")
	}
	got, err := a.ManagedSubs("n1")
	if err != nil || len(got) != 1 || got[0].ProxyCount != -1 {
		t.Fatalf("ManagedSubs: %v %v", got, err)
	}
	if err := a.RemoveSub("n1", "https://x/y"); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveSub("n1", "https://x/y"); err == nil {
		t.Error("removing twice must error")
	}
}
