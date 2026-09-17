package source

import (
	"context"
	"strings"
	"testing"
)

// diagRunner разбирает запросы в ФИКСИРОВАННОМ порядке. Обычный стаб на map здесь
// не годится: тексты карточки пользователя и проверки существования содержат общую
// подстроку «Справочник.Пользователи КАК Пользователи», а обход map случайный —
// тест был бы зелёным через раз.
type diagRunner struct {
	card     *QueryResult
	chain    *QueryResult
	roles    *QueryResult
	settings *QueryResult
	kinds    *QueryResult
	exists   *QueryResult
}

func (r *diagRunner) ExecuteQuery(_ context.Context, p QueryParams) (*QueryResult, error) {
	switch {
	case strings.Contains(p.Text, "ИдентификаторИБ"):
		return orEmpty(r.card), nil
	case strings.Contains(p.Text, "ТЧСостав"):
		return orEmpty(r.chain), nil
	case strings.Contains(p.Text, "Идент.Имя КАК Роль"):
		return orEmpty(r.roles), nil
	case strings.Contains(p.Text, "ИспользоватьУправлениеДоступом"):
		return orEmpty(r.settings), nil
	case strings.Contains(p.Text, "ТЧВиды"):
		return orEmpty(r.kinds), nil
	case strings.Contains(p.Text, "Справочник.Пользователи КАК Пользователи"):
		return orEmpty(r.exists), nil
	}
	return &QueryResult{}, nil
}

func orEmpty(r *QueryResult) *QueryResult {
	if r == nil {
		return &QueryResult{}
	}
	return r
}

func cardRow(invalid, deleted bool, ibID string) *QueryResult {
	return rows(map[string]any{
		"Наименование":    "Тест РукСТО",
		"Недействителен":  invalid,
		"ПометкаУдаления": deleted,
		"Служебный":       false,
		"ИдентификаторИБ": ibID,
	})
}

const liveIB = "6f4c1a20-0000-4000-8000-000000000001"

// healthyRunner — цепочка, которая должна пройти целиком.
func healthyRunner() *diagRunner {
	return &diagRunner{
		card:     cardRow(false, false, liveIB),
		exists:   rows(map[string]any{"Наименование": "Тест РукСТО"}),
		chain:    rows(map[string]any{"Группа": "ТЕСТ_РукСТО", "Профиль": "ТЕСТ_РукСТО"}),
		roles:    roleRows("БазовыеПрава", "РаботаСЗаказНарядами"),
		settings: settingsRow(true, false),
	}
}

func lastStep(d *AccessDiagnosis) AccessStep {
	return d.Steps[len(d.Steps)-1]
}

// TestDiagnoseStopsOnUnknownUser — обрыв на первом же звене обязан быть отличим от
// «прав нет»: чаще всего это опечатка в наименовании.
func TestDiagnoseStopsOnUnknownUser(t *testing.T) {
	d, err := AccessDiagnose(context.Background(), &diagRunner{}, "Тест Опечатка", "")
	if err != nil {
		t.Fatalf("AccessDiagnose: %v", err)
	}
	if len(d.Steps) != 1 {
		t.Fatalf("после обрыва цепочка продолжилась: %+v", d.Steps)
	}
	if lastStep(d).OK {
		t.Fatal("звено должно быть отмечено как непройденное")
	}
	if !strings.Contains(d.Verdict, "не найден") {
		t.Fatalf("вердикт = %q", d.Verdict)
	}
}

// TestDiagnoseStopsOnDisabledUser — «Недействителен» снимает вопрос о ролях целиком.
func TestDiagnoseStopsOnDisabledUser(t *testing.T) {
	r := healthyRunner()
	r.card = cardRow(true, false, liveIB)

	d, err := AccessDiagnose(context.Background(), r, "Тест РукСТО", "Документ.ЗаказНаряд")
	if err != nil {
		t.Fatalf("AccessDiagnose: %v", err)
	}
	if len(d.Steps) != 2 {
		t.Fatalf("ожидался обрыв на втором звене, шагов %d", len(d.Steps))
	}
	if !strings.Contains(d.Verdict, "Недействителен") {
		t.Fatalf("вердикт не называет причину: %q", d.Verdict)
	}
	if len(d.Roles) != 0 {
		t.Fatal("роли не должны собираться после обрыва")
	}
}

// TestDiagnoseStopsWhenNotLinkedToIBUser — элемент справочника без связи с
// пользователем ИБ: под ним нельзя войти, и роли тут ни при чём. Именно это звено
// раньше искали дольше всего, потому что в справочнике пользователь выглядит целым.
func TestDiagnoseStopsWhenNotLinkedToIBUser(t *testing.T) {
	r := healthyRunner()
	r.card = cardRow(false, false, "00000000-0000-0000-0000-000000000000")

	d, err := AccessDiagnose(context.Background(), r, "Тест РукСТО", "")
	if err != nil {
		t.Fatalf("AccessDiagnose: %v", err)
	}
	if len(d.Steps) != 3 {
		t.Fatalf("ожидался обрыв на третьем звене, шагов %d", len(d.Steps))
	}
	if !strings.Contains(d.Verdict, "не связан") {
		t.Fatalf("вердикт = %q", d.Verdict)
	}
}

func TestDiagnoseStopsWithoutAccessGroups(t *testing.T) {
	r := healthyRunner()
	r.chain = rows()

	d, err := AccessDiagnose(context.Background(), r, "Тест РукСТО", "")
	if err != nil {
		t.Fatalf("AccessDiagnose: %v", err)
	}
	if lastStep(d).Step != "группы доступа" || lastStep(d).OK {
		t.Fatalf("ожидался обрыв на группах доступа: %+v", d.Steps)
	}
}

// TestDiagnoseReportsRecordLevelOff — главный экономящий ответ: в базе с выключенным
// ограничением на уровне записей искать условие RLS незачем.
func TestDiagnoseReportsRecordLevelOff(t *testing.T) {
	d, err := AccessDiagnose(context.Background(), healthyRunner(), "Тест РукСТО", "")
	if err != nil {
		t.Fatalf("AccessDiagnose: %v", err)
	}
	for _, s := range d.Steps {
		if !s.OK {
			t.Fatalf("обрывов быть не должно: %+v", s)
		}
	}
	rls := lastStep(d)
	if rls.Step != "ограничение на уровне записей" || !strings.Contains(rls.Detail, "выключено") {
		t.Fatalf("последнее звено = %+v", rls)
	}
	if len(d.Profiles) != 1 || len(d.Roles) != 2 {
		t.Fatalf("профили %v, роли %v", d.Profiles, d.Roles)
	}
	if !strings.Contains(d.Caveat, "ПараметрыРаботыПользователей") {
		t.Fatalf("предупреждение про кэш параметров потеряно: %q", d.Caveat)
	}
	if !strings.Contains(d.NextStep, "object=") {
		t.Fatalf("без объекта нужно предложить его указать: %q", d.NextStep)
	}
}

// TestDiagnoseHandsOffToRightsAudit — последнее звено живёт в конфигурации, поэтому
// инструмент обязан вернуть конкретный следующий шаг, а не выдумать ответ про права.
func TestDiagnoseHandsOffToRightsAudit(t *testing.T) {
	d, err := AccessDiagnose(context.Background(), healthyRunner(), "Тест РукСТО", "Документ.ЗаказНаряд")
	if err != nil {
		t.Fatalf("AccessDiagnose: %v", err)
	}
	if !strings.Contains(d.NextStep, "rights_audit") || !strings.Contains(d.NextStep, "Документ.ЗаказНаряд") {
		t.Fatalf("следующий шаг не назван: %q", d.NextStep)
	}
	if strings.Contains(d.Verdict, "прав нет") {
		t.Fatalf("инструмент не должен делать вывод о правах: %q", d.Verdict)
	}
}

// TestDiagnoseDescribesAccessKindsWhenRLSOn — при включённом ограничении виды доступа
// берутся из ТИПЗНАЧЕНИЯ, а не из пустого представления.
func TestDiagnoseDescribesAccessKindsWhenRLSOn(t *testing.T) {
	r := healthyRunner()
	r.settings = settingsRow(true, true)
	r.kinds = rows(
		map[string]any{"ВидДоступа": "Организация", "ВсеРазрешены": true},
		map[string]any{"ВидДоступа": "Склад", "ВсеРазрешены": false},
	)

	d, err := AccessDiagnose(context.Background(), r, "Тест РукСТО", "")
	if err != nil {
		t.Fatalf("AccessDiagnose: %v", err)
	}
	rls := lastStep(d)
	if !strings.Contains(rls.Detail, "включено") {
		t.Fatalf("звено RLS = %+v", rls)
	}
	if !strings.Contains(rls.Detail, "Организация (все разрешены)") {
		t.Fatalf("вид доступа не расшифрован: %q", rls.Detail)
	}
	if !strings.Contains(rls.Detail, "Склад (ограничен значениями)") {
		t.Fatalf("ограниченный вид доступа не отмечен: %q", rls.Detail)
	}
}

func TestDiagnoseRequiresUser(t *testing.T) {
	if _, err := AccessDiagnose(context.Background(), &diagRunner{}, "  ", ""); err == nil {
		t.Fatal("пустой пользователь должен быть ошибкой")
	}
}
