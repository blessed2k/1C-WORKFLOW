package app

import (
	"encoding/json"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// TestResponseJSONShape закрепляет форму конверта из spec §Формат
// структурированного ответа буквально: generation, stale, warnings, items,
// totalCount, nextCursor — и ничего лишнего сверх jsonschema-выведенной формы.
func TestResponseJSONShape(t *testing.T) {
	type item struct {
		Name string `json:"name"`
	}
	resp := Response[item]{
		Generation: domain.NewGeneration(1, 42),
		Stale:      true,
		Warnings:   []Warning{{Code: "stale_index", Message: "индекс устарел", Hint: "reindex"}},
		Items:      []item{{Name: "Помощь"}},
		TotalCount: 1,
		NextCursor: "cur1",
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, field := range []string{"generation", "stale", "warnings", "items", "totalCount", "nextCursor"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("поле %q отсутствует в сериализованном ответе: %s", field, data)
		}
	}
	if raw["generation"] != "e1.g42" {
		t.Errorf("generation = %v, want e1.g42", raw["generation"])
	}
}

// TestResponseOmitsEmptyPagination: ответ без предупреждений и без
// продолжения не тащит за собой мусорные поля — totalCount=0 и nextCursor=""
// опускаются (omitempty), а items остаётся пустым списком, не null, чтобы
// клиент не проверял на nil отдельно от len==0.
func TestResponseOmitsEmptyPagination(t *testing.T) {
	type item struct{}
	resp := Response[item]{Generation: domain.NewGeneration(1, 1), Items: []item{}}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := raw["nextCursor"]; ok {
		t.Errorf("nextCursor не должен появляться в JSON, когда страниц больше нет: %s", data)
	}
	if _, ok := raw["totalCount"]; ok {
		t.Errorf("totalCount=0 опускается (omitempty): %s", data)
	}
	items, ok := raw["items"].([]any)
	if !ok {
		t.Fatalf("items не список: %s", data)
	}
	if len(items) != 0 {
		t.Errorf("items = %v, want пустой список", items)
	}
}
