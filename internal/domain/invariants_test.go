package domain_test

import (
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// корректныйSpan — заведомо валидная позиция, чтобы в тестах других инвариантов
// span не был причиной отказа.
var корректныйSpan = domain.Span{StartByte: 10, EndByte: 42, StartLine: 2, StartCol: 1, EndLine: 3, EndCol: 15}

// TestValidateResolutionОтбиваетНесогласованныйКлассЦели — инвариант §14:
// target_class определён ТОЛЬКО при resolved. Оси разделены: dynamic — это
// состояние разрешения, а не класс цели.
func TestValidateResolutionОтбиваетНесогласованныйКлассЦели(t *testing.T) {
	tests := []struct {
		name    string
		res     domain.Resolution
		target  domain.TargetClass
		отказ   bool
		вТексте string
	}{
		{name: "resolved с классом цели", res: domain.ResolutionResolved, target: domain.TargetSymbol},
		{name: "ambiguous без класса цели", res: domain.ResolutionAmbiguous},
		{name: "unresolved без класса цели", res: domain.ResolutionUnresolved},
		{name: "dynamic без класса цели", res: domain.ResolutionDynamic},
		{name: "resolved без класса цели", res: domain.ResolutionResolved, отказ: true, вТексте: "target_class"},
		{name: "dynamic с классом цели", res: domain.ResolutionDynamic, target: domain.TargetPlatform, отказ: true, вТексте: "только у resolved"},
		{name: "unresolved с классом цели", res: domain.ResolutionUnresolved, target: domain.TargetSymbol, отказ: true, вТексте: "только у resolved"},
		{name: "resolved с неизвестным классом", res: domain.ResolutionResolved, target: "модуль", отказ: true, вТексте: "target_class"},
		{name: "неизвестное состояние", res: "maybe", отказ: true, вТексте: "неизвестно"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.ValidateResolution(tc.res, tc.target)
			if tc.отказ {
				if err == nil {
					t.Fatalf("ValidateResolution(%q, %q) принято, ожидался отказ", tc.res, tc.target)
				}
				if !strings.Contains(err.Error(), tc.вТексте) {
					t.Errorf("сообщение не объясняет отказ (%q): %v", tc.вТексте, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateResolution(%q, %q): %v", tc.res, tc.target, err)
			}
		})
	}
}

// TestConfidenceГраницы — инвариант §14: доверие лежит в полуинтервале (0..1].
// Ноль означал бы факт, которому нельзя верить вовсе: такой факт не хранится.
func TestConfidenceГраницы(t *testing.T) {
	tests := []struct {
		значение domain.Confidence
		валидно  bool
	}{
		{1, true},
		{0.5, true},
		{0.01, true},
		{0, false},
		{-0.5, false},
		{1.01, false},
	}
	for _, tc := range tests {
		if got := tc.значение.Valid(); got != tc.валидно {
			t.Errorf("Confidence(%v).Valid() = %v, ожидалось %v", float64(tc.значение), got, tc.валидно)
		}
	}
}

// TestProvenanceЭвристикаНеМожетБытьТочной — запрет из спецификации: regex как
// источник фактов с confidence = 1. Именно на этом ломается Serena: догадка
// выдаётся за разбор.
func TestProvenanceЭвристикаНеМожетБытьТочной(t *testing.T) {
	tests := []struct {
		name    string
		prov    domain.Provenance
		conf    domain.Confidence
		отказ   bool
		вТексте string
	}{
		{
			name: "парсер BSL с единицей",
			prov: domain.Provenance{Source: domain.SourceBSLParser, File: "Ext/Module.bsl"},
			conf: domain.ConfidenceExact,
		},
		{
			name: "XML с единицей",
			prov: domain.Provenance{Source: domain.SourceXML, File: "Catalogs/Компании.xml"},
			conf: domain.ConfidenceExact,
		},
		{
			name: "эвристика с неполным доверием",
			prov: domain.Provenance{Source: domain.SourceHeuristic},
			conf: 0.6,
		},
		{
			name:    "эвристика с единицей",
			prov:    domain.Provenance{Source: domain.SourceHeuristic},
			conf:    domain.ConfidenceExact,
			отказ:   true,
			вТексте: "меньше 1",
		},
		{
			name:    "неизвестный источник",
			prov:    domain.Provenance{Source: "regexp"},
			conf:    0.5,
			отказ:   true,
			вТексте: "неизвестен",
		},
		{
			name:    "доверие вне полуинтервала",
			prov:    domain.Provenance{Source: domain.SourceXML},
			conf:    0,
			отказ:   true,
			вТексте: "(0..1]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.prov.Validate(tc.conf)
			if tc.отказ {
				if err == nil {
					t.Fatalf("Provenance{%s}.Validate(%v) принято, ожидался отказ", tc.prov.Source, float64(tc.conf))
				}
				if !strings.Contains(err.Error(), tc.вТексте) {
					t.Errorf("сообщение не объясняет отказ (%q): %v", tc.вТексте, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Provenance{%s}.Validate(%v): %v", tc.prov.Source, float64(tc.conf), err)
			}
		})
	}
}

// TestSpanValidateОтбиваетНевозможныеПозиции — span служит для выдачи ровно
// того фрагмента, по которому построен факт. Перевёрнутый интервал или
// нумерация с нуля означают, что клиент получит чужой текст.
func TestSpanValidateОтбиваетНевозможныеПозиции(t *testing.T) {
	tests := []struct {
		name  string
		span  domain.Span
		отказ bool
	}{
		{name: "корректная позиция", span: корректныйSpan},
		{name: "пустая позиция (факт без места в файле)", span: domain.Span{}},
		{name: "конец раньше начала в байтах", span: domain.Span{StartByte: 50, EndByte: 10, StartLine: 1, StartCol: 1, EndLine: 1, EndCol: 2}, отказ: true},
		{name: "отрицательное смещение", span: domain.Span{StartByte: -1, EndByte: 10, StartLine: 1, StartCol: 1, EndLine: 1, EndCol: 2}, отказ: true},
		{name: "строка нумеруется с нуля", span: domain.Span{StartByte: 0, EndByte: 10, StartLine: 0, StartCol: 1, EndLine: 1, EndCol: 2}, отказ: true},
		{name: "колонка нумеруется с нуля", span: domain.Span{StartByte: 0, EndByte: 10, StartLine: 1, StartCol: 0, EndLine: 1, EndCol: 2}, отказ: true},
		{name: "конечная строка раньше начальной", span: domain.Span{StartByte: 0, EndByte: 10, StartLine: 5, StartCol: 1, EndLine: 2, EndCol: 1}, отказ: true},
		{name: "конечная колонка раньше начальной в одной строке", span: domain.Span{StartByte: 0, EndByte: 10, StartLine: 3, StartCol: 9, EndLine: 3, EndCol: 2}, отказ: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.span.Validate()
			if tc.отказ && err == nil {
				t.Fatalf("Span %+v принят, ожидался отказ", tc.span)
			}
			if !tc.отказ && err != nil {
				t.Fatalf("Span %+v: %v", tc.span, err)
			}
		})
	}
}

// символ собирает заведомо валидный символ, который тесты портят по одному полю.
func символ() domain.Symbol {
	return domain.Symbol{
		UID:         domain.NewSymbolUID("ut", "cfg", "CommonModules/ОбщегоНазначения/Ext/Module.bsl", "проверитьдоступ"),
		Project:     "ut",
		Component:   "cfg",
		ModulePath:  "CommonModules/ОбщегоНазначения/Ext/Module.bsl",
		Kind:        domain.SymbolProcedure,
		NameNorm:    "проверитьдоступ",
		NameDisplay: "ПроверитьДоступ",
		Span:        корректныйSpan,
		Layer:       domain.BaseLayer("cfg"),
		Provenance:  domain.Provenance{Source: domain.SourceBSLParser, File: "CommonModules/ОбщегоНазначения/Ext/Module.bsl"},
		Confidence:  domain.ConfidenceExact,
	}
}

// TestSymbolValidateОтбиваетСломанныеСимволы — символ без uid или с
// рассогласованной парой имён ломает идентичность: по нему нельзя ни найти,
// ни сослаться.
func TestSymbolValidateОтбиваетСломанныеСимволы(t *testing.T) {
	tests := []struct {
		name    string
		портить func(*domain.Symbol)
		вТексте string
	}{
		{name: "без uid", портить: func(s *domain.Symbol) { s.UID = "" }, вТексте: "uid"},
		{name: "без name_display", портить: func(s *domain.Symbol) { s.NameDisplay = "" }, вТексте: "name_norm и name_display"},
		{name: "name_norm не из name_display", портить: func(s *domain.Symbol) { s.NameNorm = "ПроверитьДоступ" }, вТексте: "не соответствует"},
		{name: "неизвестный вид символа", портить: func(s *domain.Symbol) { s.Kind = "метод" }, вТексте: "вид"},
		{name: "перевёрнутый span", портить: func(s *domain.Symbol) {
			s.Span = domain.Span{StartByte: 9, EndByte: 1, StartLine: 1, StartCol: 1, EndLine: 1, EndCol: 2}
		}, вТексте: "span"},
		{name: "эвристика с полным доверием", портить: func(s *domain.Symbol) { s.Provenance.Source = domain.SourceHeuristic }, вТексте: "меньше 1"},
	}

	if err := символ().Validate(); err != nil {
		t.Fatalf("эталонный символ не проходит валидацию: %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := символ()
			tc.портить(&s)
			err := s.Validate()
			if err == nil {
				t.Fatalf("символ принят, ожидался отказ")
			}
			if !strings.Contains(err.Error(), tc.вТексте) {
				t.Errorf("сообщение не объясняет отказ (%q): %v", tc.вТексте, err)
			}
		})
	}
}

// ссылка собирает заведомо валидную ссылку.
func ссылка() domain.Reference {
	return domain.Reference{
		Project:     "ut",
		Component:   "cfg",
		ModulePath:  "CommonModules/ОбщегоНазначения/Ext/Module.bsl",
		NameNorm:    "проверитьдоступ",
		NameDisplay: "ПроверитьДоступ",
		Span:        корректныйSpan,
		Resolution:  domain.ResolutionResolved,
		TargetClass: domain.TargetSymbol,
		Confidence:  domain.ConfidenceExact,
		Layer:       domain.BaseLayer("cfg"),
		Provenance:  domain.Provenance{Source: domain.SourceBSLParser, File: "CommonModules/ОбщегоНазначения/Ext/Module.bsl"},
	}
}

// TestReferenceValidateОтбиваетСломанныеСсылки — ссылка несёт и цель, и
// уверенность в ней; рассогласование этих полей делает выдачу инструмента ложью.
func TestReferenceValidateОтбиваетСломанныеСсылки(t *testing.T) {
	tests := []struct {
		name    string
		портить func(*domain.Reference)
		вТексте string
	}{
		{name: "без имени", портить: func(r *domain.Reference) { r.NameNorm = "" }, вТексте: "name_norm"},
		{name: "dynamic с классом цели", портить: func(r *domain.Reference) { r.Resolution = domain.ResolutionDynamic }, вТексте: "только у resolved"},
		{name: "resolved без класса цели", портить: func(r *domain.Reference) { r.TargetClass = "" }, вТексте: "target_class"},
		{name: "неизвестное состояние разрешения", портить: func(r *domain.Reference) { r.Resolution = "guessed" }, вТексте: "неизвестно"},
		{name: "перевёрнутый span", портить: func(r *domain.Reference) {
			r.Span = domain.Span{StartByte: 9, EndByte: 1, StartLine: 1, StartCol: 1, EndLine: 1, EndCol: 2}
		}, вТексте: "span"},
		{name: "regex с полным доверием", портить: func(r *domain.Reference) { r.Provenance.Source = domain.SourceHeuristic }, вТексте: "меньше 1"},
	}

	if err := ссылка().Validate(); err != nil {
		t.Fatalf("эталонная ссылка не проходит валидацию: %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := ссылка()
			tc.портить(&r)
			err := r.Validate()
			if err == nil {
				t.Fatalf("ссылка принята, ожидался отказ")
			}
			if !strings.Contains(err.Error(), tc.вТексте) {
				t.Errorf("сообщение не объясняет отказ (%q): %v", tc.вТексте, err)
			}
		})
	}
}
