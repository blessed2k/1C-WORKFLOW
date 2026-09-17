package resolve

import (
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/query"
)

// QueryReferenceKind — вид ссылки из текста запроса (архитектура §14: kind
// таблица/поле/параметр/ВТ).
type QueryReferenceKind string

const (
	QueryRefTable     QueryReferenceKind = "table"
	QueryRefField     QueryReferenceKind = "field"
	QueryRefParameter QueryReferenceKind = "parameter"
	QueryRefTempTable QueryReferenceKind = "temp-table"
)

// QueryReferenceResult — один факт query_reference (store.QueryReference без
// query_id — его знает только вызывающий, у него identity вставленного query-узла).
type QueryReferenceResult struct {
	Kind     QueryReferenceKind
	NameNorm string
	// Span — позиция ВНУТРИ ТЕКСТА ЗАПРОСА (как у parse/query.Table/Field/
	// Parameter/TempTable.Span), не в BSL-файле: пересчёт в файловые байты —
	// дело вызывающего, он знает смещение литерала (bsl.QueryLiteral.Span)
	// относительно начала файла (тот же контракт, что query.GapMarker).
	Span domain.Span

	ObjectKey      string
	ObjectResolved bool
	// MemberKey/MemberResolved — только для Kind == QueryRefField.
	MemberKey      string
	MemberResolved bool

	Confidence domain.Confidence
}

// QueryLiteralReferences — query_reference одного текста запроса
// (bsl.QueryLiteral): группировка, которой не хватало до таска 12
// (interfaces.md, «Из таска 09»/«Из таска 08» — «query_reference для
// partial-текстов... не строится — только static», а static-результат
// возвращался ОДНИМ плоским списком без привязки к литералу, из которого
// присвоить query_id было нечем). LiteralIndex — индекс в mod.Queries: тот
// же порядок, что видит publish (index/publishderive.go), поэтому query_id,
// уже вставленный для этого литерала, находится без второго разбора текста.
type QueryLiteralReferences struct {
	LiteralIndex int
	Span         domain.Span // ql.Span литерала — байтовые координаты В ФАЙЛЕ, не в тексте запроса
	References   []QueryReferenceResult
}

// DeriveQueryReference разрешает таблицы/поля/параметры/временные таблицы
// текста запроса (internal/parse/query, таск 07) против объектов и членов
// метаданных из Env, сгруппированные по литералу-источнику. Работает ТОЛЬКО
// со статичными литералами (bsl.StaticityStatic): текст частично собранного
// запроса (Partial) — это один фрагмент конкатенации, а не полный текст, и
// разбирать его в одиночку значило бы выдавать часть за целое; сборка
// полного текста через query.GapMarker по всем фрагментам одного выражения —
// работа таска 09 (у него есть доступ ко всем токенам метода, а не только к
// одному QueryLiteral), НЕ повторный разбор здесь.
func DeriveQueryReference(mod *bsl.Module, env Env) []QueryLiteralReferences {
	if mod == nil {
		return nil
	}
	var out []QueryLiteralReferences
	for i, ql := range mod.Queries {
		if ql.Staticity != bsl.StaticityStatic {
			continue
		}
		text, ok := literalText(mod.Text(ql.Span))
		if !ok {
			continue
		}
		q, _ := query.Parse(text)
		refs := deriveQueryReferences(q, env)
		out = append(out, QueryLiteralReferences{LiteralIndex: i, Span: ql.Span, References: refs})
	}
	return out
}

func deriveQueryReferences(q *query.Query, env Env) []QueryReferenceResult {
	if q == nil {
		return nil
	}
	var out []QueryReferenceResult
	// alias -> (mtype, nameNorm) объекта, которому принадлежит таблица —
	// строится ДО полей: поле квалифицируется алиасом источника (или, если
	// алиаса не было, самим последним сегментом имени таблицы — 1С это
	// допускает: "ИЗ Справочник.Товары ВЫБРАТЬ Товары.Наименование").
	type tableObject struct {
		mtype, nameNorm string
	}
	aliasToObject := map[string]tableObject{}

	for _, t := range q.Tables {
		if t.Kind != query.TableBase && t.Kind != query.TableVirtual {
			continue
		}
		mtype, nameNorm, ok := queryTableObject(t.Name)
		res := QueryReferenceResult{Kind: QueryRefTable, NameNorm: nameNorm, Span: t.Span, Confidence: q.Confidence}
		if ok {
			if obj, found := env.Object(mtype, nameNorm); found {
				res.ObjectKey, res.ObjectResolved = obj.IdentityKey, true
			}
			key := nameNorm
			if t.Alias != "" {
				key = domain.NormalizeName(t.Alias)
			}
			aliasToObject[key] = tableObject{mtype: mtype, nameNorm: nameNorm}
		}
		out = append(out, res)
	}

	// Поле без квалификатора однозначно только при ровно одной таблице в
	// запросе — иначе владельца не определить без разбора JOIN-условий
	// (вне рамок этого прогона), и поле остаётся с ObjectResolved=false.
	var soleTable *tableObject
	if len(aliasToObject) == 1 {
		for _, v := range aliasToObject {
			t := v
			soleTable = &t
		}
	}
	for _, f := range q.Fields {
		res := QueryReferenceResult{Kind: QueryRefField, NameNorm: domain.NormalizeName(f.Name), Span: f.Span, Confidence: q.Confidence}
		var owner *tableObject
		if f.Qualifier != "" {
			if to, ok := aliasToObject[domain.NormalizeName(f.Qualifier)]; ok {
				owner = &to
			}
		} else {
			owner = soleTable
		}
		if owner != nil {
			if obj, found := env.Object(owner.mtype, owner.nameNorm); found {
				res.ObjectKey, res.ObjectResolved = obj.IdentityKey, true
			}
			if mem, found := env.Member(owner.mtype, owner.nameNorm, res.NameNorm); found {
				res.MemberKey, res.MemberResolved = mem.IdentityKey, true
			}
		}
		out = append(out, res)
	}

	for _, p := range q.Parameters {
		out = append(out, QueryReferenceResult{
			Kind: QueryRefParameter, NameNorm: domain.NormalizeName(p.Name), Span: p.Span, Confidence: q.Confidence,
		})
	}
	for _, tt := range q.TempTables {
		out = append(out, QueryReferenceResult{
			Kind: QueryRefTempTable, NameNorm: domain.NormalizeName(tt.Name), Span: tt.DefinedAt, Confidence: q.Confidence,
		})
	}
	return out
}

// queryTableObject переводит Table.Name ("Справочник.Товары" или, для
// виртуальной таблицы, "РегистрНакопления.Остатки.Остатки") в MType+имя
// объекта: первый сегмент — вид (queryPrefixToMType), второй — имя объекта.
// Третий сегмент виртуальной таблицы (сам вид Остатки/Обороты/...) сюда не
// входит — он уже отдельно доступен как Table.VirtualKind.
func queryTableObject(fullName string) (mtype, nameNorm string, ok bool) {
	parts := strings.SplitN(fullName, ".", 3)
	if len(parts) < 2 {
		return "", "", false
	}
	mtype, ok = queryPrefixToMType[domain.NormalizeName(parts[0])]
	if !ok {
		return "", "", false
	}
	return mtype, domain.NormalizeName(parts[1]), true
}

// literalText декодирует байты строкового литерала BSL (включая кавычки,
// как отдаёт mod.Text(bsl.QueryLiteral.Span)) в его значение времени
// исполнения: снимает внешние кавычки, разворачивает удвоенные кавычки
// ("" -> ") и снимает ведущие пробелы и '|' у строк продолжения — та же
// идиома, что распознаёт лексер (internal/parse/bsl/lexer.go:scanString),
// здесь в обратную сторону: не токенизация, декодирование одного уже
// найденного литерала. ok=false — переданные байты не форма "...".
func literalText(raw []byte) (string, bool) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", false
	}
	inner := raw[1 : len(raw)-1]

	var b strings.Builder
	b.Grow(len(inner))
	atLineStart := false
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		switch {
		case c == '"' && i+1 < len(inner) && inner[i+1] == '"':
			b.WriteByte('"')
			i++
			atLineStart = false
		case c == '\n':
			b.WriteByte('\n')
			atLineStart = true
		case atLineStart && (c == ' ' || c == '\t' || c == '\r'):
			// пробел перед '|' продолжения — не часть значения строки.
		case atLineStart && c == '|':
			atLineStart = false
			// сам '|' — идиома переноса, не часть значения строки.
		default:
			atLineStart = false
			b.WriteByte(c)
		}
	}
	return b.String(), true
}
