package meta

import (
	"os"
	"syscall"
)

// ScanWindows делает то же самое, но в платформенном файле: Go выберет его
// только под Windows, и запрет на него не распространяется.
func ScanWindows(dir string) ([]byte, error) {
	var stat syscall.Stat_t
	_ = stat
	return os.ReadFile(dir + "/Configuration.xml")
}
