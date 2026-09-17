package source

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DataHealth answers the question metadata cannot: which of several similar
// objects is the one the configuration actually maintains. Three registers can
// look equally suitable in the metadata tree while two have been dead since 2024
// and the third is written every day — and picking the dead one produces logic
// that is wrong only in production.
type ObjectHealth struct {
	Object    string `json:"object" jsonschema:"query table name, e.g. РегистрСведений.СотрудникНаСмене"`
	Rows      int64  `json:"rows" jsonschema:"total records"`
	DateField string `json:"dateField,omitempty" jsonschema:"field the activity was measured by"`
	First     string `json:"first,omitempty" jsonschema:"earliest record"`
	Last      string `json:"last,omitempty" jsonschema:"most recent record"`
	Last30    int64  `json:"last30" jsonschema:"records added in the last 30 days"`
	Last90    int64  `json:"last90" jsonschema:"records added in the last 90 days"`
	Verdict   string `json:"verdict" jsonschema:"живой | затухает | заброшен | пустой | без даты"`
	Error     string `json:"error,omitempty" jsonschema:"why this object could not be measured"`
}

// DataHealthReport is the comparison across the objects that were asked about.
type DataHealthReport struct {
	Objects []ObjectHealth `json:"objects"`
	Note    string         `json:"note,omitempty"`
}

// queryRunner is the slice of LiveSource this file needs, so the logic can be
// tested without a base.
type queryRunner interface {
	ExecuteQuery(ctx context.Context, params QueryParams) (*QueryResult, error)
}

// dateFieldsFor lists the fields worth trying as the "when was this written"
// column, in order, for a query table name.
func dateFieldsFor(table string) []string {
	head := table
	if i := strings.Index(table, "."); i > 0 {
		head = table[:i]
	}
	switch strings.ToLower(head) {
	case "регистрсведений", "informationregister":
		return []string{"Период", "Регистратор.Дата"}
	case "регистрнакопления", "accumulationregister",
		"регистрбухгалтерии", "accountingregister",
		"регистррасчета", "calculationregister":
		return []string{"Период", "Регистратор.Дата"}
	case "документ", "document", "журналдокументов", "documentjournal":
		return []string{"Дата"}
	case "справочник", "catalog", "планвидовхарактеристик", "chartofcharacteristictypes",
		"планссчетов", "chartofaccounts", "бизнеспроцесс", "businessprocess", "задача", "task":
		// No creation timestamp exists on these; only the row count is meaningful.
		return nil
	default:
		return []string{"Дата", "Период"}
	}
}

// DataHealth measures each object and classifies it. now is passed in so the
// windows are deterministic in tests.
func DataHealth(ctx context.Context, run queryRunner, names []string, now time.Time) (*DataHealthReport, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("pass at least one object, e.g. objects=[\"РегистрСведений.СотрудникНаСмене\"]")
	}
	report := &DataHealthReport{}
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		report.Objects = append(report.Objects, measureObject(ctx, run, name, now))
	}
	report.Note = "живой: есть записи за 30 дней; затухает: последняя запись 30-90 дней назад; заброшен: старше 90 дней"
	return report, nil
}

func measureObject(ctx context.Context, run queryRunner, table string, now time.Time) ObjectHealth {
	out := ObjectHealth{Object: table}

	for _, field := range dateFieldsFor(table) {
		res, err := run.ExecuteQuery(ctx, QueryParams{Text: activityQuery(table, field, now)})
		if err != nil {
			// A missing field means this candidate does not apply here; keep the
			// error only if nothing works at all.
			out.Error = err.Error()
			continue
		}
		if len(res.Rows) == 0 {
			continue
		}
		row := res.Rows[0]
		out.Error = ""
		out.DateField = field
		out.Rows = asInt(row["Всего"])
		out.First = asText(row["Первая"])
		out.Last = asText(row["Последняя"])
		out.Last30 = asInt(row["За30"])
		out.Last90 = asInt(row["За90"])
		out.Verdict = verdictFor(out)
		return out
	}

	// Either the object carries no timestamp, or every candidate field failed.
	res, err := run.ExecuteQuery(ctx, QueryParams{Text: "ВЫБРАТЬ КОЛИЧЕСТВО(*) КАК Всего ИЗ " + table})
	if err != nil {
		if out.Error == "" {
			out.Error = err.Error()
		}
		out.Verdict = "не измерен"
		return out
	}
	if len(res.Rows) > 0 {
		out.Rows = asInt(res.Rows[0]["Всего"])
	}
	out.Error = ""
	if out.Rows == 0 {
		out.Verdict = "пустой"
	} else {
		out.Verdict = "без даты"
	}
	return out
}

// activityQuery counts rows and the two recent windows in a single pass.
func activityQuery(table, field string, now time.Time) string {
	d30 := now.AddDate(0, 0, -30)
	d90 := now.AddDate(0, 0, -90)
	return fmt.Sprintf(`ВЫБРАТЬ
	КОЛИЧЕСТВО(*) КАК Всего,
	МАКСИМУМ(%[1]s) КАК Последняя,
	МИНИМУМ(%[1]s) КАК Первая,
	СУММА(ВЫБОР КОГДА %[1]s >= %[2]s ТОГДА 1 ИНАЧЕ 0 КОНЕЦ) КАК За30,
	СУММА(ВЫБОР КОГДА %[1]s >= %[3]s ТОГДА 1 ИНАЧЕ 0 КОНЕЦ) КАК За90
ИЗ %[4]s`, field, dateLiteral(d30), dateLiteral(d90), table)
}

func dateLiteral(t time.Time) string {
	return fmt.Sprintf("ДАТАВРЕМЯ(%d, %d, %d)", t.Year(), int(t.Month()), t.Day())
}

// verdictFor turns the counts into the one word the caller is after.
func verdictFor(h ObjectHealth) string {
	switch {
	case h.Rows == 0:
		return "пустой"
	case h.Last30 > 0:
		return "живой"
	case h.Last90 > 0:
		return "затухает"
	default:
		return "заброшен"
	}
}

func asInt(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		var parsed int64
		if _, err := fmt.Sscanf(n, "%d", &parsed); err == nil {
			return parsed
		}
	}
	return 0
}

func asText(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		return fmt.Sprint(s)
	}
}

// DataHealth implements the LiveSource-side entry point.
func (s *HTTPSource) DataHealth(ctx context.Context, names []string) (*DataHealthReport, error) {
	return DataHealth(ctx, s, names, time.Now())
}
