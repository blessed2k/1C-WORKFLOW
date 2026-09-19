package store

import (
	"context"
	"strings"
	"testing"
)

// queryPlan отдаёт EXPLAIN QUERY PLAN запроса одной строкой (детали шагов).
func queryPlan(t *testing.T, s *Store, q string, args ...any) string {
	t.Helper()
	var steps []string
	err := s.Read(context.Background(), func(tx *ReadTx) error {
		rows, err := tx.c.query(tx.ctx, `EXPLAIN QUERY PLAN `+q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		cols, err := rows.Columns()
		if err != nil {
			return err
		}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			if d, ok := vals[len(vals)-1].(string); ok {
				steps = append(steps, d)
			}
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	return strings.Join(steps, "; ")
}

// TestObjectDataEdgesInLayersUsesIndex: выборка рёбер слоёв расширений (god-
// node в режиме diff) идёт по idx_ode_kind_layer, а не полным сканом
// object_data_edge. План проверяется здесь, а не на шве app: там его не
// видно. Контроль: прежнее условие layer <> 'base' тем же способом даёт скан.
func TestObjectDataEdgesInLayersUsesIndex(t *testing.T) {
	s, _ := seeded(t)
	q, args := objectDataEdgesInLayersQuery([]string{"ext"}, nil, 0, 100)
	plan := queryPlan(t, s, q, args...)
	if !strings.Contains(plan, "idx_ode_kind_layer") {
		t.Errorf("план без idx_ode_kind_layer: %s", plan)
	}
	control := queryPlan(t, s, `SELECT id FROM object_data_edge WHERE layer <> ? ORDER BY id`, "base")
	if strings.Contains(control, "idx_ode_kind_layer") {
		t.Errorf("контроль: layer <> ? неожиданно идёт по индексу: %s", control)
	}
}
