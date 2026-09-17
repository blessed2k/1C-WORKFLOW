package store

import "database/sql"

// Row — SQL в store разрешён, нарушением быть не должен.
type Row struct {
	DB *sql.DB
}

// RegisterAccessRow и константы схемы — то, за чем resolve ходить разрешено.
type RegisterAccessRow struct {
	Mode string
}

const (
	EdgeWritesRegister = "writes-register"
	EdgeReadsRegister  = "reads-register"
)
