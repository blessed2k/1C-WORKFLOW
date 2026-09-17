//go:build freebsd || openbsd || netbsd || dragonfly || solaris || aix

package main

import (
	"runtime"
	"syscall"
)

// processRSS reports the peak resident set size of this process, which is all
// getrusage has. It is labelled "peak" and never dressed up as the current size:
// the three readings the benchmark asks for (cold, warm, after the TTL) only
// mean something if the number stops growing when the memory is handed back.
func processRSS() (int64, string, bool) {
	// Only the BSD manuals document Maxrss in kibibytes; solaris and aix count it
	// in another unit, so multiplying there would publish an invented number.
	// They stay in the build tag because the unsupported file excludes them and
	// every platform named here needs a processRSS to exist — they just decline
	// to report rather than report a figure nobody has checked.
	switch runtime.GOOS {
	case "solaris", "aix":
		return 0, "", false
	}
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, "", false
	}
	if ru.Maxrss <= 0 {
		return 0, "", false
	}
	return int64(ru.Maxrss) * 1024, "peak", true
}
