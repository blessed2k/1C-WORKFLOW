package resolve

import (
	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
)

// RefForm — синтактическая форма ссылки, определяющая, какой пункт §19.1
// к ней применяется. Вычисляется join-ом bsl.Module.References с
// bsl.Module.ManagerRefs по совпадению spans — Reference сам по себе не
// отличает «Модуль.Метод(» (1 точка) от «Коллекция.Объект.Метод(» (2 точки,
// парсер кладёт квалификатор только ближайшего сегмента), различить эти
// формы можно только сверившись с ManagerRef.
type RefForm int

const (
	// FormUnqualified — вызов без квалификатора: Метод(.
	FormUnqualified RefForm = iota
	// FormQualifiedModule — Q.Метод( c одной точкой; Q — кандидат в общий модуль.
	FormQualifiedModule
	// FormQualifiedManager — Коллекция.Объект.Метод(; менеджерный вызов.
	FormQualifiedManager
)

// RawRef — сырая ссылка на вход Resolve: синтаксическая форма плюс контекст
// места, где она встретилась. Строится BuildRawRefs из фактов bsl.Module —
// сам resolve модуль не парсит.
type RawRef struct {
	Form RefForm

	NameNorm    string
	NameDisplay string

	// Qualifier/QualifierNorm — сегмент перед точкой для FormQualifiedModule
	// ("" для остальных форм).
	Qualifier     string
	QualifierNorm string

	// ManagerMType/ManagerObjectNameNorm — заполнены только для
	// FormQualifiedManager: переведённый bslCollectionToMType вид объекта и
	// нормализованное имя объекта (второй сегмент цепочки).
	ManagerMType          string
	ManagerObjectNameNorm string

	CallerModulePath string
	CallerDirective  string
	CallerModuleKind bsl.ModuleKind

	Span domain.Span
}

// BuildRawRefs строит RawRef для каждого вызова (bsl.RefCall) модуля.
// Конструкторы (bsl.RefNew) в §19.1 не входят в пункты разрешения вызовов —
// это ссылка на ТИП, а не на метод, отдельная семантика вне рамок резолвера
// вызовов этого таска (упрощение, см. corpus-тест TestNewТипБезСкобок).
func BuildRawRefs(modulePath string, mod *bsl.Module) []RawRef {
	if mod == nil {
		return nil
	}
	modulePath = domain.NormalizeModulePath(modulePath)

	// Индекс ManagerRef по концу span члена (MemberSpan) — конец, а не
	// начало и не весь span, потому что это единственная величина, которая
	// у Reference.Span и ManagerRef.MemberSpan гарантированно совпадает
	// побайтово: это позиция одного и того же идентификатора метода.
	byMemberEnd := make(map[int][]bsl.ManagerRef)
	for _, mr := range mod.ManagerRefs {
		if mr.MemberSpan.IsZero() {
			continue
		}
		byMemberEnd[mr.MemberSpan.EndByte] = append(byMemberEnd[mr.MemberSpan.EndByte], mr)
	}

	var out []RawRef
	for _, ref := range mod.References {
		if ref.Kind != bsl.RefCall {
			continue
		}
		directive := ""
		if ref.Method != bsl.NoMethod && ref.Method < len(mod.Methods) {
			directive = mod.Methods[ref.Method].Directive
		}
		raw := RawRef{
			NameNorm:         domain.NormalizeName(mod.Name(ref.Span)),
			NameDisplay:      mod.Name(ref.Span),
			CallerModulePath: modulePath,
			CallerDirective:  directive,
			CallerModuleKind: mod.Info.Kind,
			Span:             ref.Span,
		}

		if mr, ok := matchManagerRef(byMemberEnd, ref); ok {
			raw.Form = FormQualifiedManager
			raw.ManagerMType, _ = bslCollectionToMType(mr.MetaType)
			raw.ManagerObjectNameNorm = domain.NormalizeName(mod.Name(mr.NameSpan))
			out = append(out, raw)
			continue
		}

		if !ref.QualifierSpan.IsZero() {
			raw.Form = FormQualifiedModule
			raw.Qualifier = mod.Name(ref.QualifierSpan)
			raw.QualifierNorm = domain.NormalizeName(raw.Qualifier)
			out = append(out, raw)
			continue
		}

		raw.Form = FormUnqualified
		out = append(out, raw)
	}
	return out
}

// matchManagerRef находит ManagerRef с трёхсегментной формой (Member
// заполнен), чей MemberSpan совпадает со span вызова, и чей CollectionSpan
// лежит СТРОГО раньше ref.QualifierSpan (т.е. это действительно «Коллекция.
// Объект.Метод», а не однодотный «Модуль.Метод», случайно попавший в тот же
// конец span при коллизии концов — на практике не встречается, но проверка
// дешева и убирает лишнее сомнение).
func matchManagerRef(byMemberEnd map[int][]bsl.ManagerRef, ref bsl.Reference) (bsl.ManagerRef, bool) {
	cands, ok := byMemberEnd[ref.Span.EndByte]
	if !ok {
		return bsl.ManagerRef{}, false
	}
	for _, mr := range cands {
		if mr.MemberSpan.StartByte == ref.Span.StartByte && mr.CollectionSpan.StartByte < ref.Span.StartByte {
			return mr, true
		}
	}
	return bsl.ManagerRef{}, false
}
