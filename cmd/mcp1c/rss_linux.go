//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
)

// statmPath is where the kernel publishes the memory figures of this process.
const statmPath = "/proc/self/statm"

// processRSS reports the current resident set size of this process in bytes,
// read from /proc/self/statm: its second field is the resident page count.
func processRSS() (int64, string, bool) {
	raw, err := os.ReadFile(statmPath)
	if err != nil {
		return 0, "", false
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return 0, "", false
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || pages <= 0 {
		return 0, "", false
	}
	return pages * int64(os.Getpagesize()), "current", true
}
