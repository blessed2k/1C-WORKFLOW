package bsl

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// checkSpans проверяет контракт §18.3 на всех фактах модуля: полуинтервал
// лежит внутри файла, производные координаты заполнены и согласованы,
// текст по span извлекается. Используется и на дефектных входах, и на корпусе,
// и в fuzz — инвариант один и тот же.
func checkSpans(t *testing.T, src []byte, mod *Module, diags []domain.Diagnostic, where string) {
	t.Helper()
	check := func(what string, sp domain.Span) {
		if sp.StartByte < 0 || sp.EndByte > len(src) || sp.StartByte > sp.EndByte {
			t.Fatalf("%s: %s: span %+v вне границ файла длиной %d", where, what, sp, len(src))
		}
		if err := sp.Validate(); err != nil {
			t.Fatalf("%s: %s: %v (span %+v)", where, what, err, sp)
		}
		if !sp.IsZero() && (sp.StartLine < 1 || sp.StartCol < 1 || sp.EndLine < 1 || sp.EndCol < 1) {
			t.Fatalf("%s: %s: производные координаты не заполнены: %+v", where, what, sp)
		}
	}
	for _, m := range mod.Methods {
		check("метод", m.Span)
		check("имя метода", m.NameSpan)
		check("тело метода", m.BodySpan)
		for _, prm := range m.Params {
			check("параметр", prm.Span)
			check("имя параметра", prm.NameSpan)
		}
	}
	for _, v := range mod.Variables {
		check("переменная", v.Span)
		check("имя переменной", v.NameSpan)
	}
	for _, r := range mod.References {
		check("ссылка", r.Span)
		check("квалификатор ссылки", r.QualifierSpan)
	}
	for _, r := range mod.ManagerRefs {
		check("менеджер", r.Span)
		check("имя объекта", r.NameSpan)
		check("коллекция", r.CollectionSpan)
		check("член менеджера", r.MemberSpan)
	}
	for _, q := range mod.Queries {
		check("текст запроса", q.Span)
	}
	for _, ra := range mod.RegisterAccesses {
		check("обращение к регистру", ra.Span)
		check("имя регистра", ra.NameSpan)
	}
	for _, r := range mod.Regions {
		check("область", r.Span)
		check("заголовок области", r.HeaderSpan)
	}
	for _, pp := range mod.Preprocs {
		check("препроцессор", pp.Span)
		check("текст препроцессора", pp.TextSpan)
	}
	for _, d := range diags {
		check("диагностика", d.Span)
		if d.Code == "" {
			t.Fatalf("%s: диагностика без кода: %+v", where, d)
		}
		if d.Severity == "" {
			t.Fatalf("%s: диагностика без severity: %+v", where, d)
		}
	}
}

// checkHeuristics проверяет инвариант домена: эвристический факт обязан иметь
// confidence меньше 1, а происхождение — проходить domain.Provenance.Validate.
func checkHeuristics(t *testing.T, mod *Module, where string) {
	t.Helper()
	validate := func(what string, prov domain.Provenance, conf domain.Confidence) {
		if err := prov.Validate(conf); err != nil {
			t.Fatalf("%s: %s: %v", where, what, err)
		}
	}
	for _, q := range mod.Queries {
		validate("текст запроса", q.Provenance, q.Confidence)
		if q.Confidence >= domain.ConfidenceExact {
			t.Fatalf("%s: текст запроса — эвристика, confidence=%v", where, float64(q.Confidence))
		}
	}
	for _, ra := range mod.RegisterAccesses {
		validate("обращение к регистру", ra.Provenance, ra.Confidence)
		if ra.Confidence >= domain.ConfidenceExact {
			t.Fatalf("%s: режим регистра — эвристика, confidence=%v", where, float64(ra.Confidence))
		}
	}
	for _, mr := range mod.ManagerRefs {
		validate("обращение к менеджеру", mr.Provenance, mr.Confidence)
	}
}
