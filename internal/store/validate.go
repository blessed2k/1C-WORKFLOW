package store

import (
	"context"
	"fmt"
	"sort"
)

// validate прогоняет инварианты раздела 15 плюс PRAGMA foreign_key_check.
// Возвращает карту «проверка -> число нарушений»; всё, кроме нулей, — дефект.
//
// Обе половины обязательны и не заменяют друг друга: foreign_key_check ловит
// битые ссылки, но НЕ ловит orphan-родителей — node без subtype-строки не
// нарушает ни одной FK.
func validate(ctx context.Context, c *conn) (map[string]int64, error) {
	out := make(map[string]int64, len(validateQueries)+1)
	names := make([]string, 0, len(validateQueries))
	for n := range validateQueries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		v, err := c.queryInt(ctx, validateQueries[n])
		if err != nil {
			return nil, fmt.Errorf("инвариант %s: %w", n, err)
		}
		out[n] = v
	}
	fk, err := c.queryInt(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`)
	if err != nil {
		return nil, fmt.Errorf("foreign_key_check: %w", err)
	}
	out["foreign_key_check"] = fk
	return out, nil
}

// validateFailures перечисляет нарушенные инварианты в стабильном порядке.
func validateFailures(v map[string]int64) []string {
	var bad []string
	for k, n := range v {
		if n != 0 {
			bad = append(bad, fmt.Sprintf("%s=%d", k, n))
		}
	}
	sort.Strings(bad)
	return bad
}

// integrityCheck — PRAGMA integrity_check, обязательная часть validate-шага
// перед публикацией эпохи (раздел 18.1).
func integrityCheck(ctx context.Context, c *conn) (string, error) {
	return c.queryText(ctx, "PRAGMA integrity_check")
}
