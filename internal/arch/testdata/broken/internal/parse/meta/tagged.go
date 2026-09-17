//go:build unix

package meta

import "syscall"

// Tagged отобран build tag'ом, поэтому syscall здесь законен.
func Tagged() int { return syscall.Getpid() }
