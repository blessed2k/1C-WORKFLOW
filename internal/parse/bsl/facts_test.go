package bsl

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Три вида фактов сверх переносимого спайка:
// литеральные обращения к менеджерам, тексты запросов, обращения к регистрам
// через набор/менеджер записи. Разметка ниже прочитана по тексту фикстур,
// а не снята с парсера.

// Справочники.Номенклатура.НайтиПоКоду(...) — обращение к менеджеру с третьим
// сегментом (членом). Форма синтаксическая, факт точный: confidence=1,
// provenance=parser-bsl.
func TestParseОбращениеКМенеджеруСЧленом(t *testing.T) {
	src := []byte("" +
		"Процедура Найти() Экспорт\n" +
		"\tТовар = Справочники.Номенклатура.НайтиПоКоду(\"00001\");\n" +
		"\tВозврат Товар;\n" +
		"КонецПроцедуры\n")

	mod, diags := Parse(src, Options{})
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %v", codes(diags))
	}
	checkSpans(t, src, mod, diags, "менеджер с членом")
	checkHeuristics(t, mod, "менеджер с членом")

	if len(mod.ManagerRefs) != 1 {
		t.Fatalf("обращений к менеджеру: %d, ожидалось 1", len(mod.ManagerRefs))
	}
	mr := mod.ManagerRefs[0]
	if mr.MetaType != "Catalogs" {
		t.Errorf("MetaType: %q, ожидалось Catalogs", mr.MetaType)
	}
	if got := mod.Name(mr.CollectionSpan); got != "Справочники" {
		t.Errorf("CollectionSpan: %q", got)
	}
	if got := mod.Name(mr.NameSpan); got != "Номенклатура" {
		t.Errorf("NameSpan: %q", got)
	}
	if got := mod.Name(mr.MemberSpan); got != "НайтиПоКоду" {
		t.Errorf("MemberSpan: %q", got)
	}
	if got := mod.Name(mr.Span); got != "Справочники.Номенклатура.НайтиПоКоду" {
		t.Errorf("Span: %q", got)
	}
	if mr.Confidence != domain.ConfidenceExact {
		t.Errorf("Confidence: %v, ожидался ConfidenceExact", float64(mr.Confidence))
	}
	if mr.Provenance.Source != domain.SourceBSLParser {
		t.Errorf("Provenance.Source: %q, ожидался %q", mr.Provenance.Source, domain.SourceBSLParser)
	}
	if mod.MethodName(mr.Method) != "Найти" {
		t.Errorf("Method: %q, ожидался Найти", mod.MethodName(mr.Method))
	}

	// Английский синоним и обращение без третьего сегмента: MemberSpan пуст.
	src2 := []byte("Ссылка = Catalogs.Products;\n")
	mod2, _ := Parse(src2, Options{})
	if len(mod2.ManagerRefs) != 1 {
		t.Fatalf("catalogs.products: обращений %d, ожидалось 1", len(mod2.ManagerRefs))
	}
	mr2 := mod2.ManagerRefs[0]
	if mr2.MetaType != "Catalogs" || !mr2.MemberSpan.IsZero() {
		t.Errorf("catalogs.products: MetaType=%q MemberSpan=%+v", mr2.MetaType, mr2.MemberSpan)
	}
}

// Строковый литерал, целиком являющийся текстом запроса (Запрос.Текст = "ВЫБРАТЬ...").
// Эвристика по форме, поэтому confidence < ConfidenceExact.
func TestParseСтатическийТекстЗапроса(t *testing.T) {
	src := []byte("" +
		"Процедура Заполнить() Экспорт\n" +
		"\tЗапрос = Новый Запрос;\n" +
		"\tЗапрос.Текст = \"ВЫБРАТЬ\n" +
		"\t|\tНоменклатура.Ссылка КАК Ссылка\n" +
		"\t|ИЗ\n" +
		"\t|\tСправочник.Номенклатура КАК Номенклатура\";\n" +
		"КонецПроцедуры\n")

	mod, diags := Parse(src, Options{})
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %v", codes(diags))
	}
	checkSpans(t, src, mod, diags, "статический текст запроса")
	checkHeuristics(t, mod, "статический текст запроса")

	if len(mod.Queries) != 1 {
		t.Fatalf("текстов запроса: %d, ожидалось 1", len(mod.Queries))
	}
	q := mod.Queries[0]
	if q.Staticity != StaticityStatic {
		t.Errorf("Staticity: %q, ожидался static", q.Staticity)
	}
	if q.Confidence != ConfidenceQueryStatic {
		t.Errorf("Confidence: %v, ожидался %v", float64(q.Confidence), float64(ConfidenceQueryStatic))
	}
	if q.Confidence >= domain.ConfidenceExact {
		t.Fatalf("текст запроса — эвристика, confidence не должен достигать 1")
	}
	if mod.MethodName(q.Method) != "Заполнить" {
		t.Errorf("Method: %q, ожидался Заполнить", mod.MethodName(q.Method))
	}
	text := mod.Name(q.Span)
	if len(text) < 2 || text[0] != '"' || text[len(text)-1] != '"' {
		t.Errorf("Span литерала не покрывает кавычки: %q", text)
	}

	// Фрагмент конкатенации без ВЫБРАТЬ в начале, но с ключевым словом
	// запроса и соседством с '+' — частичный текст запроса.
	src2 := []byte("" +
		"Процедура Доп()\n" +
		"\tТекст = \"ВЫБРАТЬ Номенклатура.Ссылка\" + \"\n\t|ИЗ Справочник.Номенклатура\";\n" +
		"КонецПроцедуры\n")
	mod2, _ := Parse(src2, Options{})
	if len(mod2.Queries) != 2 {
		t.Fatalf("фрагментов запроса: %d, ожидалось 2 (%+v)", len(mod2.Queries), mod2.Queries)
	}
	if mod2.Queries[0].Staticity != StaticityStatic {
		t.Errorf("первый фрагмент: %q, ожидался static", mod2.Queries[0].Staticity)
	}
	if mod2.Queries[1].Staticity != StaticityPartial {
		t.Errorf("второй фрагмент: %q, ожидался partial", mod2.Queries[1].Staticity)
	}
	if mod2.Queries[1].Confidence != ConfidenceQueryPartial {
		t.Errorf("Confidence второго фрагмента: %v, ожидался %v", float64(mod2.Queries[1].Confidence), float64(ConfidenceQueryPartial))
	}
}

// Обращение к регистру через набор записей: конструктор привязывает
// локальную переменную (эвристика), последующий Записать() даёт факт
// с confidence бонда (ConfidenceRegisterBound).
func TestParseОбращениеКРегиструЧерезНаборЗаписей(t *testing.T) {
	src := []byte("" +
		"Процедура Провести() Экспорт\n" +
		"\tНабор = РегистрыСведений.ЦеныНоменклатуры.СоздатьНаборЗаписей();\n" +
		"\tНабор.Отбор.Номенклатура.Установить(Номенклатура);\n" +
		"\tНабор.Записать();\n" +
		"КонецПроцедуры\n")

	mod, diags := Parse(src, Options{})
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %v", codes(diags))
	}
	checkSpans(t, src, mod, diags, "набор записей")
	checkHeuristics(t, mod, "набор записей")

	if len(mod.RegisterAccesses) != 1 {
		t.Fatalf("обращений к регистру: %d, ожидалось 1 (%+v)", len(mod.RegisterAccesses), mod.RegisterAccesses)
	}
	ra := mod.RegisterAccesses[0]
	if ra.MetaType != "InformationRegisters" {
		t.Errorf("MetaType: %q, ожидался InformationRegisters", ra.MetaType)
	}
	if got := mod.Name(ra.NameSpan); got != "ЦеныНоменклатуры" {
		t.Errorf("NameSpan: %q, ожидалось ЦеныНоменклатуры", got)
	}
	if ra.Mode != ModeWrite {
		t.Errorf("Mode: %q, ожидался write", ra.Mode)
	}
	if ra.Kind != AccessRecordSet {
		t.Errorf("Kind: %q, ожидался record-set", ra.Kind)
	}
	if ra.Confidence != ConfidenceRegisterBound {
		t.Errorf("Confidence: %v, ожидался %v (бонд через переменную)", float64(ra.Confidence), float64(ConfidenceRegisterBound))
	}
	if ra.Confidence >= domain.ConfidenceExact {
		t.Fatalf("режим регистра — эвристика, confidence не должен достигать 1")
	}
	if mod.MethodName(ra.Method) != "Провести" {
		t.Errorf("Method: %q, ожидался Провести", mod.MethodName(ra.Method))
	}

	// Менеджер записи регистра сведений: тот же бонд-путь, другой Kind.
	src2 := []byte("" +
		"Процедура ОбновитьОстаток()\n" +
		"\tМЗ = РегистрыСведений.Остатки.СоздатьМенеджерЗаписи();\n" +
		"\tМЗ.Записать();\n" +
		"КонецПроцедуры\n")
	mod2, _ := Parse(src2, Options{})
	if len(mod2.RegisterAccesses) != 1 {
		t.Fatalf("менеджер записи: обращений %d, ожидалось 1", len(mod2.RegisterAccesses))
	}
	if mod2.RegisterAccesses[0].Kind != AccessRecordManager {
		t.Errorf("менеджер записи: Kind %q, ожидался record-manager", mod2.RegisterAccesses[0].Kind)
	}

	// Прямое чтение через менеджер регистра, без промежуточной переменной:
	// confidence выше (ConfidenceRegisterDirect), Kind=manager, Mode=read.
	src3 := []byte("" +
		"Функция ПолучитьЦену(Ном) Экспорт\n" +
		"\tВыборка = РегистрыСведений.ЦеныНоменклатуры.Получить(Ном);\n" +
		"\tВозврат Выборка.Цена;\n" +
		"КонецФункции\n")
	mod3, _ := Parse(src3, Options{})
	if len(mod3.RegisterAccesses) != 1 {
		t.Fatalf("прямое чтение: обращений %d, ожидалось 1", len(mod3.RegisterAccesses))
	}
	ra3 := mod3.RegisterAccesses[0]
	if ra3.Mode != ModeRead || ra3.Kind != AccessManager {
		t.Errorf("прямое чтение: Mode=%q Kind=%q", ra3.Mode, ra3.Kind)
	}
	if ra3.Confidence != ConfidenceRegisterDirect {
		t.Errorf("прямое чтение: Confidence %v, ожидался %v", float64(ra3.Confidence), float64(ConfidenceRegisterDirect))
	}
}

// Движения своего объекта через ЭтотОбъект (ThisObject) это те же движения, что
// и без префикса: и raw-анализаторы, и индекс обязаны их видеть. Коллекция
// движений чужого объекта (Документ.Движения) движением этого модуля не является.
func TestParseДвиженияЧерезЭтотОбъект(t *testing.T) {
	cases := []struct {
		имя, код, регистр, фрагмент string
		есть                        bool
	}{
		{"без префикса", "Движения.Товары.Записывать = Истина;", "Товары", "Движения.Товары.Записывать", true},
		{"ЭтотОбъект", "ЭтотОбъект.Движения.Товары.Записывать = Истина;", "Товары", "ЭтотОбъект.Движения.Товары.Записывать", true},
		{"ThisObject", "ThisObject.RegisterRecords.Stock.Write = True;", "Stock", "ThisObject.RegisterRecords.Stock.Write", true},
		{"регистр ключевых слов", "этотобъект.движения.Товары.Добавить();", "Товары", "этотобъект.движения.Товары.Добавить", true},
		{"чужой объект", "Документ.Движения.Товары.Записывать = Истина;", "", "", false},
		{"цепочка до ЭтотОбъект", "Форма.ЭтотОбъект.Движения.Товары.Записать();", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.имя, func(t *testing.T) {
			src := []byte("Процедура ОбработкаПроведения(Отказ)\n\t" + c.код + "\nКонецПроцедуры\n")
			mod, diags := Parse(src, Options{})
			if len(diags) != 0 {
				t.Fatalf("диагностик быть не должно: %v", codes(diags))
			}
			checkSpans(t, src, mod, diags, c.имя)
			checkHeuristics(t, mod, c.имя)
			var движения []RegisterAccess
			for _, ra := range mod.RegisterAccesses {
				if ra.Kind == AccessMovements {
					движения = append(движения, ra)
				}
			}
			if !c.есть {
				if len(движения) != 0 {
					t.Fatalf("движений быть не должно: %s", mod.Name(движения[0].Span))
				}
				return
			}
			if len(движения) != 1 {
				t.Fatalf("движений: %d, ожидалось 1", len(движения))
			}
			ra := движения[0]
			if got := mod.Name(ra.NameSpan); got != c.регистр {
				t.Errorf("NameSpan: %q, ожидалось %q", got, c.регистр)
			}
			if got := mod.Name(ra.Span); got != c.фрагмент {
				t.Errorf("Span: %q, ожидалось %q", got, c.фрагмент)
			}
			if ra.Mode != ModeMovement || ra.Method != 0 {
				t.Errorf("Mode %q, Method %d", ra.Mode, ra.Method)
			}
		})
	}
}
