package index

import (
	"broken/internal/source"
	"broken/internal/syntax"
)

// Reindex опирается на старый слой; syntax при этом разрешён.
func Reindex() {
	_ = source.NewXMLSource
	_ = syntax.Load
}
