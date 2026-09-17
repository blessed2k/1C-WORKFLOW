package resolve

import "broken/internal/store"

// Edge заполняется константами схемы и типом факта — объявленная поверхность,
// нарушением быть не должна.
func Edge(row store.RegisterAccessRow) string {
	if row.Mode == "read" {
		return store.EdgeReadsRegister
	}
	return store.EdgeWritesRegister
}
