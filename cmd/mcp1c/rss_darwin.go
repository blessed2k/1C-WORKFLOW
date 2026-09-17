//go:build darwin

package main

import "syscall"

// processRSS reports the peak resident set size of this process in bytes.
//
// macOS has no /proc, and the current resident size lives behind mach task info,
// which means cgo. Asking ps for it would mean forking a process on every
// server_info call — in a run whose whole point is that the server stops spending
// what it does not have to, a subprocess per call is the wrong trade. getrusage
// is a syscall in this process and costs nothing.
//
// What it gives is the peak, not the current size, and it is labelled "peak" for
// that reason: it answers "how much has this process ever held", which is the
// question behind "who ate the memory", and it must never be read as "how much is
// held right now" — a number that only ever grows would make a measurement of
// memory being handed back meaningless.
func processRSS() (int64, string, bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, "", false
	}
	// darwin is the odd one out: ru_maxrss is bytes here, kibibytes on Linux and
	// the BSDs (see rss_rusage.go). No multiplier belongs on this line.
	if ru.Maxrss <= 0 {
		return 0, "", false
	}
	return int64(ru.Maxrss), "peak", true
}
