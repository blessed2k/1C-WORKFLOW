package meta

import "os"

// Indirect склеивает путь через промежуточную переменную: конкатенация не
// встроена прямо в аргумент вызова, но результат всё равно уходит в
// файловую систему — гард обязан поймать и это.
func Indirect(dir string) ([]byte, error) {
	p := dir + "/sub/x.bin"
	return os.ReadFile(p)
}
