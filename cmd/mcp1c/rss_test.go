package main

import (
	"runtime"
	"testing"
)

// rssKindByPlatform is the deal between the number and its label, written down
// from the spec and not read back out of the code: linux publishes the current
// resident size in /proc, darwin has only the peak from getrusage without cgo or
// a subprocess, and a peak handed over as "current" would quietly turn a memory
// measurement into a number that can never go down.
var rssKindByPlatform = map[string]string{
	"linux":  "current",
	"darwin": "peak",
}

func TestProcessRSS(t *testing.T) {
	bytes, kind, ok := processRSS()
	want, known := rssKindByPlatform[runtime.GOOS]

	if !ok {
		if bytes != 0 || kind != "" {
			t.Errorf("an unavailable reading must be empty; got %d/%q", bytes, kind)
		}
		if known {
			t.Fatalf("no RSS on %s, where it is available", runtime.GOOS)
		}
		return
	}
	if kind != "current" && kind != "peak" {
		t.Errorf("kind = %q, want current or peak", kind)
	}
	if known && kind != want {
		t.Errorf("kind on %s = %q, want %q", runtime.GOOS, kind, want)
	}
	// The reading is in bytes. A live Go process holding an MCP server is worth
	// megabytes, and no test process is worth sixteen gibibytes: both ends catch
	// the classic mistake here, a kibibyte figure multiplied — or not — by 1024.
	if bytes < 1<<20 {
		t.Errorf("rss = %d bytes, implausibly small: a byte count read as something else", bytes)
	}
	if bytes > 16<<30 {
		t.Errorf("rss = %d bytes, implausibly large: a kibibyte count scaled twice", bytes)
	}
}
