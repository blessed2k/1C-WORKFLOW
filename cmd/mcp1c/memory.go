package main

import (
	"fmt"
	"runtime"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// maxCacheRowsShown caps the per-entry list of the memory block. server_info is
// read by a model with a finite context, and a cache holding one entry per
// collection per export would otherwise turn the answer into a dump. The cap is
// on rows, not on bytes: the summary above it already carries the whole picture.
const maxCacheRowsShown = 10

// processStart is when this process came up. Package-level on purpose: the
// entrypoint owns the process lifecycle and is not edited to hand the time over.
var processStart = time.Now()

// memoryOutput is the memory block of server_info: what this process is holding
// and how long it has been holding it.
type memoryOutput struct {
	RSSBytes int64             `json:"rssBytes,omitempty" jsonschema:"resident set size of the process, absent where the platform does not hand it out cheaply"`
	RSSKind  string            `json:"rssKind,omitempty" jsonschema:"which number the platform gave: current or peak"`
	Uptime   string            `json:"uptime" jsonschema:"time since the process started"`
	Cache    cacheMemoryOutput `json:"cache" jsonschema:"export cache: what it holds and under which limits"`
	Go       *goMemoryOutput   `json:"go,omitempty" jsonschema:"portable Go runtime numbers, present only when the platform gave no RSS"`
}

// goMemoryOutput stands in for RSS where the platform has none to give. These
// numbers describe the Go heap, not the process, so they never travel next to an
// RSS reading: two different things under one name is worse than one thing.
type goMemoryOutput struct {
	HeapAllocBytes int64 `json:"heapAllocBytes" jsonschema:"bytes of allocated heap objects"`
	SysBytes       int64 `json:"sysBytes" jsonschema:"bytes of memory obtained from the OS by the Go runtime"`
}

// cacheMemoryOutput summarises the export cache: the counts first, the rows
// after, so the answer is useful even when the rows are cut.
type cacheMemoryOutput struct {
	Entries    int              `json:"entries" jsonschema:"cached collections, counted before any row was dropped"`
	TotalBytes int64            `json:"totalBytes" jsonschema:"estimated size of everything cached"`
	TTL        string           `json:"ttl" jsonschema:"idle TTL after which an entry is dropped; 0s means expiry is off"`
	LimitBytes int64            `json:"limitBytes" jsonschema:"memory ceiling of the cache; 0 means eviction is off"`
	Shown      []cacheRowOutput `json:"shown" jsonschema:"per-entry rows, capped"`
	Truncated  bool             `json:"truncated" jsonschema:"true when rows were left out"`
	Note       string           `json:"note,omitempty" jsonschema:"says what was left out"`
}

type cacheRowOutput struct {
	Project string `json:"project" jsonschema:"export root the entry belongs to"`
	Kind    string `json:"kind" jsonschema:"collection kind"`
	Bytes   int64  `json:"bytes" jsonschema:"estimated size of the entry"`
	Idle    string `json:"idle" jsonschema:"time since the entry was last read"`
}

// buildMemory assembles the memory block. rssOK carries the platform's answer to
// "do you have this number": a false there means the field is absent, never zero
// bytes, and the portable runtime numbers step in so the block still says
// something. Entries arrive in the stable order CacheSnapshot documents, and the
// cap is applied to that order rather than to one invented here.
func buildMemory(rssBytes int64, rssKind string, rssOK bool, uptime time.Duration, stats source.CacheStats) memoryOutput {
	out := memoryOutput{
		Uptime: uptime.Round(time.Second).String(),
		Cache: cacheMemoryOutput{
			Entries:    len(stats.Entries),
			TotalBytes: stats.TotalBytes,
			TTL:        stats.TTL.Round(time.Second).String(),
			LimitBytes: stats.LimitBytes,
			Shown:      []cacheRowOutput{},
		},
	}
	if rssOK {
		out.RSSBytes, out.RSSKind = rssBytes, rssKind
	} else {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		out.Go = &goMemoryOutput{HeapAllocBytes: int64(ms.HeapAlloc), SysBytes: int64(ms.Sys)}
	}

	shown := stats.Entries
	if len(shown) > maxCacheRowsShown {
		shown = shown[:maxCacheRowsShown]
		out.Cache.Truncated = true
		out.Cache.Note = fmt.Sprintf("показано записей: %d из %d; сводка выше считает все",
			maxCacheRowsShown, len(stats.Entries))
	}
	for _, e := range shown {
		out.Cache.Shown = append(out.Cache.Shown, cacheRowOutput{
			Project: e.Root,
			Kind:    e.Kind,
			Bytes:   e.Bytes,
			Idle:    e.Idle.Round(time.Second).String(),
		})
	}
	return out
}
