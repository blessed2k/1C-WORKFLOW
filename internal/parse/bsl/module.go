package bsl

import "github.com/blessed2k/1C-WORKFLOW/internal/domain"

// Коды диагностик парсера BSL. Общие коды workspace живут в domain;
// коды разбора BSL принадлежат этому пакету. Диагностика опознаётся по коду:
// сверять её по формулировке сообщения нельзя.
const (
	// DiagUnclosedMethod — у метода нет закрывающего ключевого слова.
	DiagUnclosedMethod = "bsl_unclosed_method"
	// DiagUnclosedString — строковый литерал не закрыт.
	DiagUnclosedString = "bsl_unclosed_string"
	// DiagUnclosedDate — литерал даты не закрыт.
	DiagUnclosedDate = "bsl_unclosed_date"
	// DiagMethodWithoutName — за ключевым словом объявления нет имени.
	DiagMethodWithoutName = "bsl_method_without_name"
	// DiagMethodWithoutParams — у объявления нет списка параметров.
	DiagMethodWithoutParams = "bsl_method_without_params"
	// DiagParamListUnclosed — список параметров не закрыт.
	DiagParamListUnclosed = "bsl_param_list_unclosed"
	// DiagBadParameter — в списке параметров стоит не имя параметра.
	DiagBadParameter = "bsl_bad_parameter"
	// DiagUnclosedRegion — область препроцессора не закрыта.
	DiagUnclosedRegion = "bsl_unclosed_region"
	// DiagBadAnnotationArgument — скобки у аннотации расширения есть, но
	// аргументом в них стоит не имя метода (число, выражение) либо скобка
	// не закрыта. Аннотация при этом сохраняется: HasArg=true, Arg="".
	DiagBadAnnotationArgument = "bsl_bad_annotation_argument"
)

// NoMethod — значение поля Method у факта, встреченного вне методов модуля.
const NoMethod = -1

// NoRegion — значение поля Region у символа вне областей препроцессора.
const NoRegion = -1

// Уверенность эвристических фактов парсера. Все три величины — эвристики
// по построению, и domain.Provenance.Validate запрещает им confidence = 1.
const (
	// ConfidenceQueryStatic — литерал целиком выглядит текстом запроса.
	ConfidenceQueryStatic domain.Confidence = 0.9
	// ConfidenceQueryPartial — литерал участвует в склейке текста запроса.
	ConfidenceQueryPartial domain.Confidence = 0.6
	// ConfidenceRegisterDirect — режим выведен по имени метода платформы.
	ConfidenceRegisterDirect domain.Confidence = 0.95
	// ConfidenceRegisterBound — режим выведен через локальную переменную метода.
	ConfidenceRegisterBound domain.Confidence = 0.85
)

// ReferenceKind — вид ссылки на имя.
type ReferenceKind string

const (
	// RefCall — вызов метода.
	RefCall ReferenceKind = "call"
	// RefNew — конструктор Новый Тип, со скобками и без.
	RefNew ReferenceKind = "new"
)

// Parameter — параметр метода.
type Parameter struct {
	Index      int    // порядковый номер в списке, с нуля
	Name       string // как записано в коде
	NameNorm   string // domain.NormalizeName(Name)
	ByValue    bool   // объявлен через Знач/Val
	HasDefault bool
	Default    string      // сырой текст значения по умолчанию
	Span       domain.Span // весь параметр, включая Знач и значение по умолчанию
	NameSpan   domain.Span
}

// Method — процедура или функция модуля.
//
// Имя материализуется в строку прямо здесь, в отличие от ссылок: символов
// на порядок меньше (на выгрузке УТ 224 тыс. против 1.8 млн ссылок), а имя
// символа нужно любому потребителю и попадает в uid.
type Method struct {
	Name        string
	NameNorm    string
	Kind        domain.SymbolKind // domain.SymbolProcedure либо domain.SymbolFunction
	Export      bool
	Async       bool
	Directive   string       // директива компиляции, например "&НаКлиентеНаСервереБезКонтекста"
	Annotations []Annotation // аннотации расширения, в порядке исходника
	Params      []Parameter
	Span        domain.Span // от директивы/ключевого слова до КонецПроцедуры включительно
	NameSpan    domain.Span
	BodySpan    domain.Span // тело без заголовка и без закрывающего ключевого слова
	Complete    bool        // найдено закрывающее ключевое слово
	Region      int         // индекс области в Module.Regions либо NoRegion
}

// Annotation — аннотация расширения (&Перед/&После/&Вместо/&ИзменениеИКонтроль)
// вместе с аргументом: именем перехватываемого метода. Аргумент есть не у
// каждой аннотации, отсутствие аргумента — не ошибка.
//
// Срез структур, а не два параллельных среза и не карта: метод несёт несколько
// аннотаций, карта теряет соответствие при повторе имени, параллельные срезы
// расходятся при первой правке. HasArg отдельно от Arg != "", потому что
// пустые скобки `&Вместо()` надо отличать от их отсутствия `&Вместо`.
//
// ПРЕДЕЛ, названный СОЗНАТЕЛЬНО: собственного Span у аннотации нет, и он не
// выводится из модели. Диагностика DiagBadAnnotationArgument привязана к спану
// ТОКЕНА в исходнике (parse.go), а Method.Span/NameSpan описывают весь метод,
// поэтому у метода с НЕСКОЛЬКИМИ аннотациями сопоставить диагностику с
// конкретным элементом Annotations средствами модели нельзя: остаётся только
// сравнение байтовых смещений с текстом через Module.Text. Потребители
// (resolve.DeriveIntercepts) сегодня обходят весь срез и строят факт на каждую
// аннотацию, им точка диагностики не нужна, — но выводить из этого, что
// сопоставление возможно, нельзя. Путь снятия предела: завести Span в этой
// структуре и заполнять его там же, где разбирается аргумент; смысл остальных
// полей при этом не меняется.
type Annotation struct {
	Name   string // как в исходнике, с амперсандом: "&Вместо"
	Arg    string // аргумент без кавычек; "" если его нет либо он не разобран
	HasArg bool   // скобки были — отличает `&Вместо()` от `&Вместо`
}

// Variable — переменная модуля (оператор Перем вне методов).
type Variable struct {
	Name     string
	NameNorm string
	Export   bool
	Span     domain.Span // весь оператор Перем ... ; — общий для всех имён оператора
	NameSpan domain.Span
	Region   int
}

// Reference — вызов или конструктор, найденный в коде.
// Строки и комментарии сюда не попадают: они отсечены лексером.
//
// Факт хранит только позиции: ссылок на выгрузке около двух миллионов, и
// материализация имени, квалификатора и имени метода утроила бы аллокации
// разбора. Текст берётся один раз на границе store через Module.Text.
type Reference struct {
	Span          domain.Span // имя
	QualifierSpan domain.Span // сегмент слева от точки; IsZero для unqualified
	Kind          ReferenceKind
	Method        int // индекс метода-владельца в Module.Methods либо NoMethod
}

// ManagerRef — литеральное обращение к менеджеру объекта метаданных:
// Справочники.Товары, Документы.РеализацияТоваровУслуг, ПланыОбмена.X
// и их английские синонимы. Форма синтаксическая, поэтому факт точный.
type ManagerRef struct {
	MetaType       string      // канонический каталог выгрузки: Catalogs, Documents, ...
	Span           domain.Span // фрагмент «Коллекция.Имя»
	CollectionSpan domain.Span
	NameSpan       domain.Span
	MemberSpan     domain.Span // третий сегмент, если он есть; иначе IsZero
	Method         int
	Confidence     domain.Confidence
	Provenance     domain.Provenance
}

// Staticity — насколько текст запроса известен статически (истории спецификации 36–37).
type Staticity string

const (
	// StaticityStatic — литерал целиком задаёт текст запроса.
	StaticityStatic Staticity = "static"
	// StaticityPartial — литерал участвует в конкатенации, часть текста неизвестна.
	StaticityPartial Staticity = "partial"
)

// QueryLiteral — строковый литерал, помеченный эвристикой как текст запроса.
// Признак эвристический по построению, поэтому Confidence всегда меньше 1.
// Сам текст достаётся из Span через QueryText.
type QueryLiteral struct {
	Span       domain.Span // литерал целиком, включая кавычки
	Staticity  Staticity
	Method     int
	Confidence domain.Confidence
	Provenance domain.Provenance
}

// RegisterMode — как код обращается к регистру. Значения совпадают
// с CHECK таблицы register_access (internal/store).
type RegisterMode string

const (
	// ModeRead — чтение регистра.
	ModeRead RegisterMode = "read"
	// ModeWrite — запись набора записей или менеджера записи.
	ModeWrite RegisterMode = "write"
	// ModeMovement — запись движений документа.
	ModeMovement RegisterMode = "movement"
	// ModeClear — очистка набора записей.
	ModeClear RegisterMode = "clear"
)

// RegisterAccessKind — через что идёт обращение к регистру.
type RegisterAccessKind string

const (
	// AccessRecordSet — набор записей: СоздатьНаборЗаписей.
	AccessRecordSet RegisterAccessKind = "record-set"
	// AccessRecordManager — менеджер записи: СоздатьМенеджерЗаписи.
	AccessRecordManager RegisterAccessKind = "record-manager"
	// AccessManager — метод менеджера регистра: Получить, СрезПоследних, Выбрать.
	AccessManager RegisterAccessKind = "manager"
	// AccessMovements — коллекция Движения объекта документа.
	AccessMovements RegisterAccessKind = "movements"
)

// RegisterAccess — обращение к регистру через набор записей, менеджер записи,
// менеджер регистра или коллекцию Движения. Режим выводится по имени метода
// платформы, то есть эвристикой: Confidence всегда меньше 1.
type RegisterAccess struct {
	MetaType   string      // InformationRegisters, AccumulationRegisters, ...; пусто для Движения
	NameSpan   domain.Span // имя регистра
	Span       domain.Span // фрагмент, по которому построен факт
	Mode       RegisterMode
	Kind       RegisterAccessKind
	Static     bool // имя регистра взято из литерала пути, а не вычислено
	Method     int
	Confidence domain.Confidence
	Provenance domain.Provenance
}

// Region — область препроцессора #Область.
type Region struct {
	Name       string
	NameNorm   string
	Span       domain.Span // от #Область до #КонецОбласти включительно
	HeaderSpan domain.Span // строка #Область Имя
	Closed     bool
}

// PreprocKind — вид строки препроцессора.
type PreprocKind string

const (
	// PreprocRegion — #Область.
	PreprocRegion PreprocKind = "region"
	// PreprocEndRegion — #КонецОбласти.
	PreprocEndRegion PreprocKind = "end-region"
	// PreprocIf — #Если.
	PreprocIf PreprocKind = "if"
	// PreprocElsIf — #ИначеЕсли.
	PreprocElsIf PreprocKind = "elsif"
	// PreprocElse — #Иначе.
	PreprocElse PreprocKind = "else"
	// PreprocEndIf — #КонецЕсли.
	PreprocEndIf PreprocKind = "end-if"
	// PreprocInsert — #Вставка расширения.
	PreprocInsert PreprocKind = "insert"
	// PreprocEndInsert — #КонецВставки расширения.
	PreprocEndInsert PreprocKind = "end-insert"
	// PreprocDelete — #Удаление расширения.
	PreprocDelete PreprocKind = "delete"
	// PreprocEndDelete — #КонецУдаления расширения.
	PreprocEndDelete PreprocKind = "end-delete"
	// PreprocOther — прочая строка препроцессора.
	PreprocOther PreprocKind = "other"
)

// Preproc — строка препроцессора, как она встретилась в файле.
type Preproc struct {
	Kind PreprocKind
	Span domain.Span // вся строка препроцессора
	// TextSpan — условие или имя области; IsZero, если их нет.
	TextSpan domain.Span
}

// Module — факты одного BSL-модуля.
type Module struct {
	Info             ModuleInfo
	Methods          []Method
	Variables        []Variable
	References       []Reference
	ManagerRefs      []ManagerRef
	Queries          []QueryLiteral
	RegisterAccesses []RegisterAccess
	// HTTPCalls — исходящие HTTP-вызовы (httpcalls.go, веха В2).
	HTTPCalls []HTTPCall
	Regions   []Region
	Preprocs  []Preproc

	src   []byte
	lines *LineIndex
}

// Text возвращает сырые байты, покрытые позицией. Возвращается подслайс
// исходника: копия делается потребителем ровно там, где нужна строка.
func (m *Module) Text(sp domain.Span) []byte {
	if m == nil || sp.StartByte < 0 || sp.EndByte > len(m.src) || sp.StartByte > sp.EndByte {
		return nil
	}
	return m.src[sp.StartByte:sp.EndByte]
}

// Name возвращает текст позиции строкой — удобная форма Text для имён.
func (m *Module) Name(sp domain.Span) string { return string(m.Text(sp)) }

// Lines — индекс строк файла: тем же контрактом пользуются потребители,
// которым нужны координаты фрагментов вне выданных фактов.
func (m *Module) Lines() *LineIndex { return m.lines }

// Source — сырые байты разобранного модуля.
func (m *Module) Source() []byte { return m.src }

// RegionName возвращает имя области по индексу; для NoRegion — пустую строку.
func (m *Module) RegionName(i int) string {
	if i < 0 || i >= len(m.Regions) {
		return ""
	}
	return m.Regions[i].Name
}

// MethodName возвращает имя метода по индексу; для NoMethod — пустую строку.
func (m *Module) MethodName(i int) string {
	if i < 0 || i >= len(m.Methods) {
		return ""
	}
	return m.Methods[i].Name
}

// Options — настройки разбора.
type Options struct {
	// File — путь модуля относительно корня компонента, со слешами.
	// Из него выводятся вид модуля и владелец (ModuleInfo), он же попадает
	// в provenance фактов и в diagnostic.
	File string
	// SkipReferences отключает сбор ссылок, обращений к менеджерам, текстов
	// запросов и обращений к регистрам: символьному индексу они не нужны,
	// а разбор без них заметно дешевле.
	SkipReferences bool
}
