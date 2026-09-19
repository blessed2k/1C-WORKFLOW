package bsl

import (
	"bytes"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// registerBinding — локальная переменная метода, за которой закреплён регистр:
// Набор = РегистрыСведений.Х.СоздатьНаборЗаписей(). Связь локальная и потому
// эвристическая: факты, построенные по ней, несут confidence меньше 1.
type registerBinding struct {
	collection string
	metaType   string
	name       string
	nameSpan   domain.Span // span имени регистра в точке создания привязки
	kind       RegisterAccessKind
}

// collectManagerRef распознаёт литеральное обращение к менеджеру объекта
// метаданных: Справочники.Товары, Документы.РеализацияТоваровУслуг,
// ПланыОбмена.X, включая третий сегмент (Справочники.Х.НайтиПоКоду), и их
// английские синонимы. Форма синтаксическая, поэтому факт точный: confidence
// = ConfidenceExact, provenance = parser-bsl.
func (p *parser) collectManagerRef(i int) {
	t := p.toks[i]
	if i > 0 && isPunct(p.toks[i-1], '.') {
		return // это не начало обращения, а сегмент чужой цепочки
	}
	metaType, ok := lookupCollection(string(t.lit))
	if !ok {
		return
	}
	if i+2 >= len(p.toks) || !isPunct(p.toks[i+1], '.') || p.toks[i+2].kind != tokIdent {
		return
	}
	nameTok := p.toks[i+2]
	ref := ManagerRef{
		MetaType:       metaType,
		CollectionSpan: p.li.Span(t.sp.start, t.sp.end),
		NameSpan:       p.li.Span(nameTok.sp.start, nameTok.sp.end),
		Method:         p.method,
		Confidence:     domain.ConfidenceExact,
		Provenance:     domain.Provenance{Source: domain.SourceBSLParser, File: p.opts.File},
	}
	end := nameTok.sp.end
	if i+4 < len(p.toks) && isPunct(p.toks[i+3], '.') && p.toks[i+4].kind == tokIdent {
		memberTok := p.toks[i+4]
		ref.MemberSpan = p.li.Span(memberTok.sp.start, memberTok.sp.end)
		end = memberTok.sp.end
	}
	ref.Span = p.li.Span(t.sp.start, end)
	p.mod.ManagerRefs = append(p.mod.ManagerRefs, ref)
}

// queryKeywordPrefixes — ключевые слова, с которых начинается текст запроса.
var queryKeywordPrefixes = [][]byte{[]byte("ВЫБРАТЬ"), []byte("SELECT")}

// queryKeywords — признаки текста запроса внутри фрагмента, участвующего
// в конкатенации.
var queryKeywords = [][]byte{
	[]byte("ВЫБРАТЬ"), []byte("SELECT"),
	[]byte("ИЗ "), []byte(" FROM "),
	[]byte("ГДЕ "), []byte(" WHERE "),
	[]byte("ОБЪЕДИНИТЬ"), []byte("UNION"),
}

// stringLiteralInner отрезает кавычки строкового литерала. Экранированные
// кавычки ("") внутри текста для этой эвристики не разворачиваются: признак
// смотрит только на начало и на подстроки, удвоенная кавычка ему не мешает.
func stringLiteralInner(lit []byte) []byte {
	if len(lit) < 2 {
		return nil
	}
	return lit[1 : len(lit)-1]
}

// queryLiteralHead возвращает начало литерала для проверки на ключевое слово
// запроса: пробелы и переводы строк снимаются, а один ведущий '|' — маркер
// продолжения многострочного литерала на следующей строке (типовая идиома
// БСП: открывающая кавычка одна, само «ВЫБРАТЬ» начинается уже после '|' на
// следующей строке) — снимается тоже, после чего пробелы снимаются повторно.
func queryLiteralHead(inner []byte) []byte {
	s := bytes.TrimLeft(inner, " \t\r\n")
	if len(s) > 0 && s[0] == '|' {
		s = bytes.TrimLeft(s[1:], " \t\r\n")
	}
	return s
}

func hasQueryKeywordPrefix(s []byte) bool {
	for _, kw := range queryKeywordPrefixes {
		if len(s) >= len(kw) && bytes.EqualFold(s[:len(kw)], kw) {
			return true
		}
	}
	return false
}

func containsQueryKeyword(s []byte) bool {
	upper := bytes.ToUpper(s)
	for _, kw := range queryKeywords {
		if bytes.Contains(upper, bytes.ToUpper(kw)) {
			return true
		}
	}
	return false
}

// adjacentToConcat сообщает, стоит ли по соседству с токеном i оператор '+':
// признак, что строковый литерал — фрагмент конкатенации, а не целый текст.
func (p *parser) adjacentToConcat(i int) bool {
	if i > 0 && isPunct(p.toks[i-1], '+') {
		return true
	}
	if i+1 < len(p.toks) && isPunct(p.toks[i+1], '+') {
		return true
	}
	return false
}

// collectQueryLiteral эвристически размечает строковый литерал как текст
// запроса. Признак — форма содержимого (начинается с ВЫБРАТЬ/SELECT, в том
// числе после '|' на следующей строке — Static; либо соседствует с '+' и
// содержит ключевое слово запроса — Partial, фрагмент конкатенации). Это
// угадывание по тексту, а не разбор языка запросов, поэтому confidence
// всегда меньше ConfidenceExact.
func (p *parser) collectQueryLiteral(i int) {
	t := p.toks[i]
	inner := stringLiteralInner(t.lit)
	sp := p.li.Span(t.sp.start, t.sp.end)
	switch {
	case hasQueryKeywordPrefix(queryLiteralHead(inner)):
		p.mod.Queries = append(p.mod.Queries, QueryLiteral{
			Span:       sp,
			Staticity:  StaticityStatic,
			Method:     p.method,
			Confidence: ConfidenceQueryStatic,
			Provenance: domain.Provenance{Source: domain.SourceHeuristic, File: p.opts.File, Detail: "query-literal-static"},
		})
	case p.adjacentToConcat(i) && containsQueryKeyword(inner):
		p.mod.Queries = append(p.mod.Queries, QueryLiteral{
			Span:       sp,
			Staticity:  StaticityPartial,
			Method:     p.method,
			Confidence: ConfidenceQueryPartial,
			Provenance: domain.Provenance{Source: domain.SourceHeuristic, File: p.opts.File, Detail: "query-literal-partial"},
		})
	}
}

// registerReadMethods — методы менеджера регистра, читающие данные без
// промежуточной переменной: режим выводится прямо по имени метода платформы.
var registerReadMethods = []string{
	"Получить", "Get",
	"СрезПоследних", "SliceLast",
	"СрезПервых", "SliceFirst",
	"Выбрать", "Select",
	"ПолучитьОстатки", "GetBalances",
}

// collectRegisterAccess распознаёт обращения к регистру: через менеджер
// напрямую (РегистрыСведений.Х.Получить), через набор записей или менеджер
// записи, привязанные к локальной переменной метода (Набор = ....СоздатьНаборЗаписей();
// ... Набор.Записать()), и через коллекцию Движения документа. Режим всегда
// выводится по имени метода платформы — эвристика, confidence < ConfidenceExact.
func (p *parser) collectRegisterAccess(i int) {
	t := p.toks[i]
	if eqAny(t.lit, "Движения", "RegisterRecords") {
		if start, ok := p.movementsStart(i); ok {
			p.collectMovementAccess(i, start)
		}
		return
	}
	if i > 0 && isPunct(p.toks[i-1], '.') {
		return // сегмент чужой цепочки, не начало обращения
	}
	if metaType, ok := lookupCollection(string(t.lit)); ok && isRegisterCollection(metaType) {
		p.collectDirectRegisterAccess(i, metaType)
		return
	}
	p.collectBoundRegisterAccess(i)
}

// collectDirectRegisterAccess разбирает Коллекция.Регистр.Метод(...). Для
// конструкторов набора/менеджера записи заводит привязку локальной переменной
// (см. bindRegisterVar); для методов чтения менеджера сразу даёт факт.
func (p *parser) collectDirectRegisterAccess(i int, metaType string) {
	t := p.toks[i]
	if i+4 >= len(p.toks) || !isPunct(p.toks[i+1], '.') || p.toks[i+2].kind != tokIdent ||
		!isPunct(p.toks[i+3], '.') || p.toks[i+4].kind != tokIdent {
		return
	}
	regTok := p.toks[i+2]
	methodTok := p.toks[i+4]
	regSpan := p.li.Span(regTok.sp.start, regTok.sp.end)

	switch {
	case eqAny(methodTok.lit, "СоздатьНаборЗаписей", "CreateRecordSet"):
		p.bindRegisterVar(i, metaType, string(regTok.lit), regSpan, AccessRecordSet)
	case eqAny(methodTok.lit, "СоздатьМенеджерЗаписи", "CreateRecordManager"):
		p.bindRegisterVar(i, metaType, string(regTok.lit), regSpan, AccessRecordManager)
	case eqAny(methodTok.lit, registerReadMethods...):
		p.mod.RegisterAccesses = append(p.mod.RegisterAccesses, RegisterAccess{
			MetaType:   metaType,
			NameSpan:   regSpan,
			Span:       p.li.Span(t.sp.start, methodTok.sp.end),
			Mode:       ModeRead,
			Kind:       AccessManager,
			Static:     true,
			Method:     p.method,
			Confidence: ConfidenceRegisterDirect,
			Provenance: domain.Provenance{Source: domain.SourceHeuristic, File: p.opts.File, Detail: "register-manager-" + string(methodTok.lit)},
		})
	}
}

// bindRegisterVar запоминает, что локальная переменная метода получила
// результат конструктора набора записей или менеджера записи: Х = Коллекция.Регистр.Создать...().
// Привязка действует до конца метода (parseMethod сбрасывает p.binds).
func (p *parser) bindRegisterVar(collIdx int, metaType, regName string, regSpan domain.Span, kind RegisterAccessKind) {
	if collIdx < 2 || !isPunct(p.toks[collIdx-1], '=') || p.toks[collIdx-2].kind != tokIdent {
		return
	}
	varName := string(p.toks[collIdx-2].lit)
	if p.binds == nil {
		p.binds = make(map[string]registerBinding)
	}
	p.binds[domain.NormalizeName(varName)] = registerBinding{
		collection: metaType,
		metaType:   metaType,
		name:       regName,
		nameSpan:   regSpan,
		kind:       kind,
	}
}

// collectBoundRegisterAccess проверяет, не является ли токен i локальной
// переменной, привязанной к регистру (bindRegisterVar), и если следующий
// вызов — Записать/Прочитать/Очистить, даёт факт с confidence бонда
// (ConfidenceRegisterBound): режим выведен через переменную, а не напрямую.
func (p *parser) collectBoundRegisterAccess(i int) {
	if p.binds == nil {
		return
	}
	b, ok := p.binds[domain.NormalizeName(string(p.toks[i].lit))]
	if !ok {
		return
	}
	if i+2 >= len(p.toks) || !isPunct(p.toks[i+1], '.') || p.toks[i+2].kind != tokIdent {
		return
	}
	methodTok := p.toks[i+2]
	var mode RegisterMode
	switch {
	case eqAny(methodTok.lit, "Записать", "Write"):
		mode = ModeWrite
	case eqAny(methodTok.lit, "Прочитать", "Read"):
		mode = ModeRead
	case eqAny(methodTok.lit, "Очистить", "Clear"):
		mode = ModeClear
	default:
		return
	}
	t := p.toks[i]
	p.mod.RegisterAccesses = append(p.mod.RegisterAccesses, RegisterAccess{
		MetaType:   b.metaType,
		NameSpan:   b.nameSpan,
		Span:       p.li.Span(t.sp.start, methodTok.sp.end),
		Mode:       mode,
		Kind:       b.kind,
		Static:     false,
		Method:     p.method,
		Confidence: ConfidenceRegisterBound,
		Provenance: domain.Provenance{Source: domain.SourceHeuristic, File: p.opts.File, Detail: "register-bound-" + string(methodTok.lit)},
	})
}

// movementsStart решает, чья коллекция Движения стоит на позиции i, и
// возвращает индекс токена, с которого начинается обращение. Своя коллекция
// модуля: Движения в начале выражения либо ЭтотОбъект.Движения
// (ThisObject.RegisterRecords), и тогда обращение начинается с ЭтотОбъект.
// Движения чужого объекта (Документ.Движения, Форма.ЭтотОбъект.Движения)
// движениями этого модуля не являются.
func (p *parser) movementsStart(i int) (int, bool) {
	if i == 0 || !isPunct(p.toks[i-1], '.') {
		return i, true
	}
	if i >= 2 && p.toks[i-2].kind == tokIdent && eqAny(p.toks[i-2].lit, "ЭтотОбъект", "ThisObject") &&
		(i == 2 || !isPunct(p.toks[i-3], '.')) {
		return i - 2, true
	}
	return 0, false
}

// collectMovementAccess разбирает [ЭтотОбъект.]Движения.Регистр[.Метод]:
// коллекцию движений документа; start: первый токен обращения. MetaType
// пуст: это не менеджер объекта метаданных, а свойство объекта документа
// (см. комментарий RegisterAccess.MetaType).
func (p *parser) collectMovementAccess(i, start int) {
	if i+2 >= len(p.toks) || !isPunct(p.toks[i+1], '.') || p.toks[i+2].kind != tokIdent {
		return
	}
	regTok := p.toks[i+2]
	end := regTok.sp.end
	if i+4 < len(p.toks) && isPunct(p.toks[i+3], '.') && p.toks[i+4].kind == tokIdent {
		end = p.toks[i+4].sp.end
	}
	p.mod.RegisterAccesses = append(p.mod.RegisterAccesses, RegisterAccess{
		MetaType:   "",
		NameSpan:   p.li.Span(regTok.sp.start, regTok.sp.end),
		Span:       p.li.Span(p.toks[start].sp.start, end),
		Mode:       ModeMovement,
		Kind:       AccessMovements,
		Static:     true,
		Method:     p.method,
		Confidence: ConfidenceRegisterDirect,
		Provenance: domain.Provenance{Source: domain.SourceHeuristic, File: p.opts.File, Detail: "movements"},
	})
}
