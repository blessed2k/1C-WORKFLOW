//go:build go1.1

package fake

// FakePath используется TestГардВидитФиктивныйBuildTag: тег go1.1 не
// ограничивает платформу (это версия языка, а не GOOS), поэтому файл обязан
// остаться под проверкой CheckPortability наравне с обычным файлом.
const FakePath = "/tmp/fake"
