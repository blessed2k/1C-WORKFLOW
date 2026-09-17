package retrieve

import "broken/internal/app"

// Lookup тянет app напрямую — направление зависимостей обратное, запрещено.
func Lookup() {
	_ = app.Lookup
}
