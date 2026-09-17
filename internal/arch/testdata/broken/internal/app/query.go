package app

import "database/sql"

// Lookup ходит в SQL мимо store — запрещено.
func Lookup(db *sql.DB, name string) *sql.Row {
	return db.QueryRow("SELECT 1 FROM symbol WHERE name = ?", name)
}
