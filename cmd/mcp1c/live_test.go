package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLiveStateSwitch(t *testing.T) {
	ls := newLiveState()
	ls.add(baseConfig{Name: "wms", URL: "http://wms"})
	ls.add(baseConfig{Name: "trade", URL: "http://trade"})

	if ls.firstName() != "wms" {
		t.Errorf("firstName = %q, want wms (insertion order)", ls.firstName())
	}
	if _, err := ls.setBase("trade"); err != nil {
		t.Fatalf("setBase: %v", err)
	}
	if ls.currentName() != "trade" {
		t.Errorf("current = %q, want trade", ls.currentName())
	}
	if ls.live() == nil || ls.source() == nil {
		t.Error("live/source nil after setBase")
	}
	if _, err := ls.setBase("nope"); err == nil {
		t.Error("expected error for unknown base")
	}

	info := ls.listInfo() // sorted by name: trade, wms
	if len(info) != 2 || info[0].Name != "trade" || !info[0].Current || info[1].Current {
		t.Errorf("listInfo = %+v", info)
	}
}

func TestLiveStateLoadFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "bases.json")
	body := `[{"name":"a","url":"http://a","user":"u","password":"p"},{"name":"b","url":"http://b"}]`
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ls := newLiveState()
	if err := ls.loadFile(f); err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	if len(ls.listInfo()) != 2 {
		t.Errorf("bases = %d, want 2", len(ls.listInfo()))
	}
	// entries missing name or url are skipped
	if ls.live() != nil {
		t.Error("no base should be selected before setBase")
	}
}
