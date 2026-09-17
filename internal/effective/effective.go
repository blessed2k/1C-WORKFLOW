// Package effective содержит единственную реализацию effective-вида модуля,
// то есть наложение слоёв расширений на модуль базового компонента (ADR-4,
// docs/architecture-index.md §13/§20).
//
// Зачем отдельный пакет. Наложение нужно и internal/app (get_symbol,
// get_module_structure, find_impact, граф), и internal/retrieve
// (get_context_for_task), а retrieve не имеет права импортировать app
// (гард CheckRetrieveNotApp). Раньше это кончилось двумя почти построчными
// копиями, у которых уже разошлись порядок расширений (манифест против
// store) и тексты подсказок. Пакет стоит ниже обоих и опирается только на
// domain, parse/bsl, resolve и store.
//
// Перехватчики вычисляются НА ЧТЕНИИ и не материализуются: каждый вызов
// заново разбирает исходник заимствовавшего модуля (SkipReferences: true,
// вызовы внутри перехватчика не нужны) и связывает перехватчик с базовым
// методом по имени цели из аргумента аннотации (resolve.DeriveIntercepts).
// Стоимость ограничена числом расширений, применяющихся к базе, а не
// размером проекта.
//
// Транзакций пакет не открывает (ADR-005/ADR-014): источник слоёв передаёт
// вызывающий, и для store это адаптер над уже открытой read-транзакцией.
package effective

import (
	"errors"
	"fmt"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
)

// codeBlobUnavailable: код диагностики «модуль расширения заимствован, но
// его исходник не прочитался». Код публичный, его отдают оба инструмента.
const codeBlobUnavailable = "effective_blob_unavailable"

// codeInsteadConflict: код предупреждения о нескольких &Вместо на один метод.
const codeInsteadConflict = "instead_conflict"

// Extension: расширение и базовый компонент, к которому оно применяется.
// Свой тип, а не store.Component: правило порядка и in-memory источник не
// должны зависеть от строки хранилища.
type Extension struct {
	ID         string
	AppliesTo  string
	ApplyOrder int
}

// Source: всё, что модулю нужно от хранилища. Интерфейс объявлен здесь,
// а не взят конкретным типом store: иначе сбой чтения blob посреди прохода
// нечем воспроизвести в тесте, и диагностика, написанная ради того, чтобы
// сбой не выглядел пустотой, осталась бы непроверенной.
type Source interface {
	// ExtensionsApplyingTo: расширения с AppliesTo == base в порядке
	// применения. Порядок задаёт источник, модуль его не пересортировывает.
	ExtensionsApplyingTo(base string) ([]Extension, error)
	// ModuleText: исходник модуля modulePath в расширении ext. found=false:
	// расширение модуль не заимствовало. found=true и text=nil: модуль
	// заимствован, но строки файла нет; перехватчиков и диагностики тогда
	// нет. Нечитаемый исходник заимствованного
	// модуля: ошибка, обёрнутая в *BlobUnavailableError; любая другая ошибка
	// прерывает вычисление целиком.
	ModuleText(ext, modulePath string) (text []byte, found bool, err error)
}

// BlobUnavailableError отделяет «модуль заимствован, но исходник не
// прочитался» (диагностика, проход продолжается) от отказа индекса
// (вычисление прерывается).
type BlobUnavailableError struct{ Err error }

func (e *BlobUnavailableError) Error() string { return e.Err.Error() }
func (e *BlobUnavailableError) Unwrap() error { return e.Err }

// Intercept: факт перехвата и точный текст перехватчика, вырезанный из того
// же исходника, что разбирался для аннотаций: второй выборки по символу
// расширения не нужно.
type Intercept struct {
	resolve.Intercept
	Text string
}

// Notice: нейтральный текст предупреждения. app и retrieve переводят его в
// свои типы Warning один к одному.
type Notice struct {
	Code    string
	Message string
	Hint    string
}

// Result: effective-вид одного модуля.
type Result struct {
	// Intercepts: перехватчики всех методов модуля в порядке расширений.
	Intercepts []Intercept
	// BorrowedBy: расширения, заимствовавшие модуль, даже без единого
	// перехватчика: вызывающему нужно отличать «расширения нет» от
	// «расширение есть, но этот метод не перехватывает».
	BorrowedBy []string
	// Diagnostics: нечитаемые исходники и отказы DeriveIntercepts (ADR-027).
	Diagnostics []Notice
}

// Module накладывает на модуль modulePath компонента base все применяющиеся
// к нему расширения.
func Module(src Source, base, modulePath string) (Result, error) {
	exts, err := src.ExtensionsApplyingTo(base)
	if err != nil {
		return Result{}, err
	}
	var r Result
	for _, ext := range exts {
		text, found, err := src.ModuleText(ext.ID, modulePath)
		var unavailable *BlobUnavailableError
		if errors.As(err, &unavailable) {
			r.BorrowedBy = append(r.BorrowedBy, ext.ID)
			r.Diagnostics = append(r.Diagnostics, Notice{
				Code:    codeBlobUnavailable,
				Message: fmt.Sprintf("не удалось прочитать модуль расширения %s (%s): %v", ext.ID, modulePath, unavailable.Err),
				Hint:    "blob мог быть вычищен по TTL: вызовите reindex",
			})
			continue
		}
		if err != nil {
			return Result{}, err
		}
		if !found {
			continue
		}
		r.BorrowedBy = append(r.BorrowedBy, ext.ID)
		if text == nil {
			continue
		}
		mod, _ := bsl.Parse(text, bsl.Options{File: modulePath, SkipReferences: true})
		layer := domain.ExtensionLayer(domain.ComponentID(ext.ID), ext.ApplyOrder)
		derived, diags := resolve.DeriveIntercepts(modulePath, mod, layer)
		// Отказ строить факт перехвата (ADR-027) обязан быть виден: иначе
		// «перехватчиков нет» и «цель перехватчика не разобрана» для агента
		// неразличимы. По методу диагностика не фильтруется: цели у неё нет
		// по определению.
		for _, d := range diags {
			r.Diagnostics = append(r.Diagnostics, Notice{
				Code:    d.Code,
				Message: fmt.Sprintf("расширение %s: %s", ext.ID, d.Message),
				Hint: "поправьте аннотацию в модуле расширения: без имени цели перехватчик не связывается с методом базового слоя; " +
					"исходник перехватчика: get_module_structure component=" + ext.ID + " view=effective",
			})
		}
		for _, ic := range derived {
			r.Intercepts = append(r.Intercepts, Intercept{Intercept: ic, Text: spanText(text, ic.InterceptorSpan)})
		}
	}
	return r, nil
}

func spanText(text []byte, sp domain.Span) string {
	if sp.StartByte >= 0 && sp.EndByte <= len(text) && sp.StartByte <= sp.EndByte {
		return string(text[sp.StartByte:sp.EndByte])
	}
	return ""
}

// InsteadConflict: текст конфликта &Вместо (ADR-4: diagnostic со всеми
// слоями и confidence < 1, а не молчаливый выбор одного слоя). Один на оба
// инструмента: об одном факте они обязаны говорить одинаково.
func InsteadConflict(c resolve.InterceptConflict) Notice {
	names := make([]string, len(c.Layers))
	for i, l := range c.Layers {
		names[i] = string(l.Component)
	}
	return Notice{
		Code: codeInsteadConflict,
		Message: fmt.Sprintf(
			"метод %q перехвачен через &Вместо несколькими расширениями (%s) — порядок применения расширений из XML не выводится, итоговое поведение статически неопределимо (confidence=%.1f)",
			c.TargetNameNorm, strings.Join(names, ", "), float64(c.Confidence)),
		Hint: "фактический порядок расширений смотрите в конфигураторе (Configuration -> Extensions): индекс его не знает (ADR-4, docs/tools-index.md)",
	}
}
