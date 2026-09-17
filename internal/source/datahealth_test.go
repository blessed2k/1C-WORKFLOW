package source

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// scriptedRunner answers queries from a script keyed by a substring of the query
// text, and records everything it was asked.
type scriptedRunner struct {
	answers map[string]*QueryResult // substring -> result
	fails   []string                // substrings that must return an error
	asked   []string
}

func (s *scriptedRunner) ExecuteQuery(_ context.Context, p QueryParams) (*QueryResult, error) {
	s.asked = append(s.asked, p.Text)
	for _, f := range s.fails {
		if strings.Contains(p.Text, f) {
			return nil, errors.New(`Поле не найдено "` + f + `"`)
		}
	}
	for key, res := range s.answers {
		if strings.Contains(p.Text, key) {
			return res, nil
		}
	}
	return &QueryResult{Rows: []map[string]any{{"Всего": float64(0)}}}, nil
}

func row(total, last30, last90 float64, first, last string) *QueryResult {
	return &QueryResult{Rows: []map[string]any{{
		"Всего": total, "За30": last30, "За90": last90, "Первая": first, "Последняя": last,
	}}}
}

var testNow = time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

// TestDataHealthPicksTheLiveRegister is the scenario this tool exists for: three
// registers look equally plausible in the metadata, two are dead, and the wrong
// choice only shows up in production.
func TestDataHealthPicksTheLiveRegister(t *testing.T) {
	runner := &scriptedRunner{answers: map[string]*QueryResult{
		"РегистрСведений.СотрудникНаСмене":   row(33322, 5100, 15000, "2024-01-10", "2026-07-27"),
		"РегистрСведений.СтатусыСотрудников": row(2069, 0, 0, "2024-02-01", "2024-11-30"),
		"РегистрСведений.ЛокацииСотрудников": row(0, 0, 0, "", ""),
	}}
	rep, err := DataHealth(context.Background(), runner, []string{
		"РегистрСведений.СотрудникНаСмене",
		"РегистрСведений.СтатусыСотрудников",
		"РегистрСведений.ЛокацииСотрудников",
	}, testNow)
	if err != nil {
		t.Fatalf("DataHealth: %v", err)
	}
	if len(rep.Objects) != 3 {
		t.Fatalf("measured %d objects, want 3", len(rep.Objects))
	}
	want := map[string]string{
		"РегистрСведений.СотрудникНаСмене":   "живой",
		"РегистрСведений.СтатусыСотрудников": "заброшен",
		"РегистрСведений.ЛокацииСотрудников": "пустой",
	}
	for _, o := range rep.Objects {
		if got := want[o.Object]; got != o.Verdict {
			t.Errorf("%s: verdict %q, want %q", o.Object, o.Verdict, got)
		}
	}
}

func TestDataHealthFadingRegister(t *testing.T) {
	runner := &scriptedRunner{answers: map[string]*QueryResult{
		"РегистрСведений.Тихий": row(500, 0, 12, "2025-01-01", "2026-06-15"),
	}}
	rep, _ := DataHealth(context.Background(), runner, []string{"РегистрСведений.Тихий"}, testNow)
	if rep.Objects[0].Verdict != "затухает" {
		t.Errorf("verdict = %q, want затухает", rep.Objects[0].Verdict)
	}
}

// TestDataHealthFallsBackToRegistrar: an information register without a Период
// column is measured through its recorder instead of being reported as broken.
func TestDataHealthFallsBackToRegistrar(t *testing.T) {
	runner := &scriptedRunner{
		fails: []string{"МАКСИМУМ(Период)"},
		answers: map[string]*QueryResult{
			"Регистратор.Дата": row(10, 3, 8, "2026-05-01", "2026-07-20"),
		},
	}
	rep, _ := DataHealth(context.Background(), runner, []string{"РегистрСведений.ПоДокументу"}, testNow)
	got := rep.Objects[0]
	if got.DateField != "Регистратор.Дата" {
		t.Errorf("DateField = %q, want Регистратор.Дата", got.DateField)
	}
	if got.Verdict != "живой" || got.Error != "" {
		t.Errorf("fallback did not produce a clean result: %+v", got)
	}
}

// TestDataHealthCatalogHasNoTimestamp: catalogs carry no creation date, so the
// count is the honest answer rather than a made-up window.
func TestDataHealthCatalogHasNoTimestamp(t *testing.T) {
	runner := &scriptedRunner{answers: map[string]*QueryResult{
		"Справочник.Номенклатура": {Rows: []map[string]any{{"Всего": float64(1200)}}},
	}}
	rep, _ := DataHealth(context.Background(), runner, []string{"Справочник.Номенклатура"}, testNow)
	got := rep.Objects[0]
	if got.Rows != 1200 || got.Verdict != "без даты" {
		t.Errorf("catalog measured wrong: %+v", got)
	}
	for _, q := range runner.asked {
		if strings.Contains(q, "За30") {
			t.Errorf("catalog should not be queried for date windows: %s", q)
		}
	}
}

func TestDataHealthDocumentUsesДата(t *testing.T) {
	runner := &scriptedRunner{answers: map[string]*QueryResult{
		"Документ.ЗаказКлиента": row(90, 4, 20, "2026-01-01", "2026-07-26"),
	}}
	rep, _ := DataHealth(context.Background(), runner, []string{"Документ.ЗаказКлиента"}, testNow)
	if rep.Objects[0].DateField != "Дата" {
		t.Errorf("DateField = %q, want Дата", rep.Objects[0].DateField)
	}
}

// TestDataHealthWindowsAreRelativeToNow pins the date arithmetic: the 30-day
// window has to be a real date literal, not a parameter the connector rejects.
func TestDataHealthWindowsAreRelativeToNow(t *testing.T) {
	runner := &scriptedRunner{}
	DataHealth(context.Background(), runner, []string{"Документ.Тест"}, testNow)
	if len(runner.asked) == 0 {
		t.Fatal("no query was issued")
	}
	q := runner.asked[0]
	if !strings.Contains(q, "ДАТАВРЕМЯ(2026, 6, 27)") {
		t.Errorf("30-day window wrong; query: %s", q)
	}
	if !strings.Contains(q, "ДАТАВРЕМЯ(2026, 4, 28)") {
		t.Errorf("90-day window wrong; query: %s", q)
	}
}

func TestDataHealthRejectsEmptyInput(t *testing.T) {
	if _, err := DataHealth(context.Background(), &scriptedRunner{}, nil, testNow); err == nil {
		t.Error("expected an error when no objects are given")
	}
}

// TestDataHealthReportsUnmeasurableObject: a name that does not resolve must come
// back as an error on that row, not as a zero that reads like "empty register".
func TestDataHealthReportsUnmeasurableObject(t *testing.T) {
	runner := &scriptedRunner{fails: []string{"РегистрСведений.НетТакого"}}
	rep, _ := DataHealth(context.Background(), runner, []string{"РегистрСведений.НетТакого"}, testNow)
	got := rep.Objects[0]
	if got.Error == "" || got.Verdict != "не измерен" {
		t.Errorf("unmeasurable object reported as data: %+v", got)
	}
}
