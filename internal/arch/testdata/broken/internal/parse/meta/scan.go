package meta

import (
	"os"
	"syscall"
)

// Scan непортируем четырежды: syscall в общем коде, unix-only каталог,
// сравнение разделителя со слэшем и склейка пути литералом.
func Scan(dir string) ([]byte, error) {
	var stat syscall.Stat_t
	if err := syscall.Stat(dir, &stat); err != nil {
		return nil, err
	}
	if dir == "" {
		dir = "/tmp/выгрузка"
	}
	if os.PathSeparator == '/' {
		dir += "."
	}
	return os.ReadFile(dir + "/Configuration.xml")
}
