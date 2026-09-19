// Package query разбирает текст запроса 1С (язык запросов, не BSL) в список
// используемых таблиц (включая виртуальные и временные), полей, параметров и
// временных таблиц. Парсер терпимый: он не паникует ни на каком входе и
// выдаёт то, что смог понять, честно помечая неполноту через staticity и
// confidence — вместо падения или молчаливой выдумки фактов.
//
// Вызывающий код (сборка текста запроса из BSL-выражения: литерал, конкатенация
// литерала с переменными, полностью динамическое выражение) в зону этого
// пакета не входит. Если часть текста запроса собрана не литералом, вызывающий
// код обязан заменить неизвестный фрагмент на GapMarker перед вызовом Parse —
// см. документацию константы.
package query

import (
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// GapMarker — плейсхолдер одного неизвестного статически фрагмента текста
// запроса (переменная, вызов функции, результат конкатенации, чьё значение
// Parse получить не может). У Parse нет доступа к BSL AST: единственный вход —
// строка text, поэтому граница «литерал / не литерал» обязана быть закодирована
// в самой строке этим маркером — вызывающий код подставляет его вместо
// нелитеральной части выражения перед вызовом Parse.
//
// NUL-байт выбран по тому же соображению, что и разделитель в
// domain.NewSymbolUID: он не встречается в тексте запроса 1С. Несколько подряд
// идущих маркеров считаются одним пробелом неизвестного размера.
const GapMarker = "\x00"

// Staticity: насколько текст запроса известен статически (архитектура §14).
type Staticity string

const (
	// StaticityStatic — текст запроса целиком литерал: разобрано с confidence,
	// равным domain.ConfidenceExact.
	StaticityStatic Staticity = "static"
	// StaticityPartial — текст собран из литерала и GapMarker-заглушек:
	// разобрано то, что есть, confidence < 1.
	StaticityPartial Staticity = "partial"
	// StaticityDynamic — текст запроса статически неизвестен (пуст или состоит
	// из одних заглушек): разбирать нечего, confidence < 1.
	StaticityDynamic Staticity = "dynamic"
)

// упрощение: доверие нестатичных фактов — фиксированные уровни, а не формула
// от доли известного текста. Формула точнее, но здесь не нужна: staticity уже
// несёт основной сигнал, а два уровня доверия дают предсказуемое поведение
// нижестоящим потребителям (index, retrieve). Путь наверх — доля
// len(text)-gapBytes к len(text), если понадобится более тонкая шкала.
const (
	confidencePartial domain.Confidence = 0.6
	confidenceDynamic domain.Confidence = 0.2
)

// Диагностические коды этого пакета.
const (
	// DiagUnterminatedString — строковый литерал текста запроса не закрыт до
	// конца входа; разбор продолжается до конца текста.
	DiagUnterminatedString = "query_unterminated_string"
	// DiagInvalidConfidence — внутренняя проверка инварианта
	// domain.Provenance.Validate не прошла; сигнал о дефекте в самом Parse, а
	// не тихая порча факта.
	DiagInvalidConfidence = "query_invalid_confidence"
)

// TableKind — вид источника в секции ИЗ/FROM.
type TableKind string

const (
	// TableBase — обычная таблица объекта метаданных.
	TableBase TableKind = "base"
	// TableVirtual — виртуальная таблица регистра (Обороты, Остатки,
	// ОстаткиИОбороты, СрезПоследних, СрезПервых).
	TableVirtual TableKind = "virtual"
	// TableTemp — временная таблица, связанная с более ранним ПОМЕСТИТЬ в том
	// же тексте.
	TableTemp TableKind = "temp"
	// TableSubquery — источник, заданный вложенным запросом в скобках.
	TableSubquery TableKind = "subquery"
	// TableParameter — источник, заданный параметром запроса напрямую
	// (ИЗ &Параметр КАК Алиас — внешняя временная таблица).
	TableParameter TableKind = "parameter"
)

// Table — один источник данных секции ИЗ/FROM или СОЕДИНЕНИЕ/JOIN.
type Table struct {
	// Name — имя источника как оно написано в тексте: полное точечное имя для
	// TableBase/TableVirtual ("Справочник.Номенклатура"), имя без точек для
	// TableTemp, имя параметра без "&" для TableParameter, "" для
	// TableSubquery.
	Name string
	// Alias — псевдоним (после КАК/AS или, для скобочного подзапроса и
	// параметра, без него — в 1С алиас может идти без КАК). "" если алиаса нет.
	Alias string
	Kind  TableKind
	// VirtualKind — вид виртуальной таблицы ("Остатки", "Обороты",
	// "ОстаткиИОбороты", "СрезПоследних", "СрезПервых"); "" если Kind не
	// TableVirtual.
	VirtualKind string
	// Params — сырой текст параметров виртуальной таблицы в скобках, как
	// написан (без внешних скобок); "" если скобок не было или они пустые.
	Params string
	// Span — позиция имени источника (для TableVirtual — включая скобки
	// параметров, для TableSubquery — позиция всей скобочной группы) в тексте,
	// переданном в Parse.
	Span domain.Span
}

// Field — обращение к полю в списке выборки (секция ВЫБРАТЬ/SELECT), с
// квалификатором и алиасом.
type Field struct {
	// Qualifier — то, что стоит до точки (алиас источника или имя таблицы);
	// "" если поле указано без квалификатора.
	Qualifier string
	// Name — имя поля после точки, либо имя целиком, если квалификатора нет.
	Name string
	// Alias — псевдоним после КАК/AS; "" если алиаса нет.
	Alias string
	// Span — позиция выражения "[Qualifier.]Name" в тексте.
	Span domain.Span
}

// Parameter — обращение к параметру запроса (&Имя), где бы оно ни встретилось:
// в условиях, в параметрах виртуальной таблицы, в качестве источника.
type Parameter struct {
	// Name — имя параметра без ведущего "&".
	Name string
	Span domain.Span
}

// TempTable — временная таблица, определённая ПОМЕСТИТЬ/INTO в тексте, со
// всеми найденными последующими использованиями через ИЗ/FROM.
type TempTable struct {
	// Name — имя временной таблицы, как написано в ПОМЕСТИТЬ.
	Name string
	// DefinedAt — позиция имени в ПОМЕСТИТЬ ИмяВТ.
	DefinedAt domain.Span
	// UsedAt — позиции всех источников ИЗ/FROM/СОЕДИНЕНИЕ, чьё имя совпало
	// (регистронезависимо, domain.NormalizeName) с именем временной таблицы.
	UsedAt []domain.Span
	// Dropped — временная таблица упомянута в УНИЧТОЖИТЬ/DROP.
	Dropped bool
}

// Query — результат разбора текста запроса 1С.
type Query struct {
	// Text — исходный текст, переданный в Parse, без изменений.
	Text       string
	Staticity  Staticity
	Confidence domain.Confidence
	Provenance domain.Provenance

	Tables     []Table
	Fields     []Field
	Parameters []Parameter
	TempTables []TempTable
}

// Parse разбирает текст запроса 1С. Чистая функция: диска не касается, ничего
// не паникует. text может содержать GapMarker — см. его документацию.
func Parse(text string) (*Query, []domain.Diagnostic) {
	var diags []domain.Diagnostic

	li := newLineIndex(text)
	toks, unterminated := tokenize(text)
	for _, u := range unterminated {
		diags = append(diags, domain.Diagnostic{
			Code:     DiagUnterminatedString,
			Severity: domain.SeverityWarning,
			Message:  "строковый литерал текста запроса не закрыт до конца входа",
			Span:     spanFor(text, li, u.start, u.end),
		})
	}

	params := scanParamRefs(text, toks, li)
	facts := parseTokens(text, toks, li)
	tables, tempTables := linkTempTables(facts.tables, facts.tempDefs, facts.tempDrops)

	q := &Query{
		Text:       text,
		Tables:     tables,
		Fields:     facts.fields,
		Parameters: params,
		TempTables: tempTables,
	}

	hasGap := strings.Contains(text, GapMarker)
	switch {
	case text == "":
		q.Staticity = StaticityDynamic
		q.Confidence = confidenceDynamic
	case !hasGap:
		q.Staticity = StaticityStatic
		q.Confidence = domain.ConfidenceExact
	case len(q.Tables) > 0 || len(q.Fields) > 0 || len(q.Parameters) > 0:
		q.Staticity = StaticityPartial
		q.Confidence = confidencePartial
	default:
		q.Staticity = StaticityDynamic
		q.Confidence = confidenceDynamic
	}
	q.Provenance = domain.Provenance{Source: domain.SourceQueryParser}

	if err := q.Provenance.Validate(q.Confidence); err != nil {
		// Не должно случиться ни при каких text: страховка от собственного
		// дефекта, а не ожидаемая ветка. Диагностика вместо тихого нарушения
		// инварианта или паники.
		diags = append(diags, domain.Diagnostic{
			Code:     DiagInvalidConfidence,
			Severity: domain.SeverityError,
			Message:  "внутренняя ошибка разбора запроса: " + err.Error(),
		})
	}

	return q, diags
}

// linkTempTables связывает источники ИЗ/FROM с определениями ПОМЕСТИТЬ того же
// текста по нормализованному имени (регистр не важен) и переводит совпавшие
// Table.Kind в TableTemp.
//
// упрощение: если одна и та же временная таблица определяется дважды
// (пересоздание после УНИЧТОЖИТЬ), под общим именем остаётся только последнее
// определение — использования между первым и вторым ПОМЕСТИТЬ привязываются к
// нему же. Раздельное отслеживание по интервалам позиций для честного
// tolerant-парсера избыточно; редкий случай в реальных текстах.
func linkTempTables(rawTables []Table, defs []tempDef, drops []string) ([]Table, []TempTable) {
	tempTables := make([]TempTable, 0, len(defs))
	for _, d := range defs {
		tempTables = append(tempTables, TempTable{Name: d.name, DefinedAt: d.span})
	}
	byName := make(map[string]int, len(tempTables))
	for i, tt := range tempTables {
		byName[domain.NormalizeName(tt.Name)] = i
	}
	for _, name := range drops {
		if idx, ok := byName[domain.NormalizeName(name)]; ok {
			tempTables[idx].Dropped = true
		}
	}

	tables := make([]Table, len(rawTables))
	copy(tables, rawTables)
	for i := range tables {
		if tables[i].Kind != TableBase || strings.Contains(tables[i].Name, ".") {
			continue
		}
		idx, ok := byName[domain.NormalizeName(tables[i].Name)]
		if !ok {
			continue
		}
		tables[i].Kind = TableTemp
		tempTables[idx].UsedAt = append(tempTables[idx].UsedAt, tables[i].Span)
	}
	return tables, tempTables
}
