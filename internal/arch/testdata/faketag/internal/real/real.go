//go:build windows

package real

// RealPath используется TestГардВидитФиктивныйBuildTag: windows — настоящий
// платформенный терм, файл обязан остаться вне проверки CheckPortability.
const RealPath = "/tmp/real"
