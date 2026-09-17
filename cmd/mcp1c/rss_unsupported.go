//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly && !solaris && !aix

package main

// processRSS has no cheap portable reading on this platform — Windows among them,
// where it would mean a syscall into psapi. The block reports the Go runtime
// numbers instead and says nothing it cannot prove: a missing field is honest,
// a zero would be a measurement that never happened.
func processRSS() (int64, string, bool) { return 0, "", false }
