package meta

// Facts — факты, извлечённые из ОДНОГО файла выгрузки. ParseFile разбирает
// файлы по отдельности (чистая функция без доступа к диску): у составных
// сущностей 1С (общий модуль = XML со свойствами + Module.bsl; форма =
// объявление в XML владельца + Form.xml) каждый файл даёт свой аспект, а
// склейка аспектов в одну логическую identity — дело индексного пайплайна
// (архитектура §14, §15; таск 09), не этого пакета.
type Facts struct {
	// Object — объект метаданных, когда файл его корень (Classify ==
	// KindMetadataObject): справочник, документ, регистр, общий модуль, роль,
	// регламентное задание, EPF/ERF-корень и так далее.
	Object *MetadataObjectFact
	// Members — реквизиты, ресурсы, измерения, значения перечисления и
	// табличные части объекта (только когда Object != nil).
	Members []MetadataMemberFact
	// FormDecls — формы, объявленные владельцем: обычно ссылки <Form>Имя</Form>
	// внутри объекта, для CommonForm — форма, которой является сам объект.
	FormDecls []FormDeclFact
	// Commands — команды объекта (ссылки <Command>Имя</Command>) и общие
	// команды (когда Object.MType == "CommonCommand").
	Commands []ObjectCommandFact

	// FormStructure — структура формы (Classify == KindFormStructure).
	FormStructure *FormStructureFact

	// ModuleRegistry — свойства общего модуля (Object.MType == "CommonModule").
	ModuleRegistry *ModuleRegistryFact

	// Document — свойства документа (Object.MType == "Document"), сегодня
	// только декларированные метаданными движения.
	Document *DocumentFact

	// HTTPService — корневой URL, шаблоны и методы HTTP-сервиса
	// (Object.MType == "HTTPService").
	HTTPService *HTTPServiceFact

	// Subscription — подписка на событие (Classify == KindEventSubscription).
	Subscription *EventSubscriptionFact

	// ScheduledJob — регламентное задание (Object.MType == "ScheduledJob").
	ScheduledJob *ScheduledJobFact

	// Role — идентичность роли (Object.MType == "Role").
	Role *RoleFact
	// RoleRights — права роли (Classify == KindRoleRights).
	RoleRights *RoleRightsFact

	// Predefined — предопределённые элементы (Classify == KindPredefinedData).
	Predefined []PredefinedItemFact
}

// MetadataObjectFact — объект метаданных верхнего уровня.
type MetadataObjectFact struct {
	// MType — вид объекта: имя XML-элемента под <MetaDataObject> (Catalog,
	// Document, InformationRegister, CommonModule, Role, ScheduledJob,
	// ExternalDataProcessor, ExternalReport, ...).
	MType string
	UUID  string
	// NameNorm/NameDisplay — нормализованное и оригинальное имя (domain.NormalizeName).
	NameNorm    string
	NameDisplay string
	Synonym     string
	Comment     string
	// Props — плоские скалярные свойства объекта (Hierarchical, Posting,
	// CodeLength, InformationRegisterPeriodicity, WriteMode, RegisterType, ...),
	// как написаны в выгрузке: имя элемента -> текст.
	Props map[string]string
}

// MetadataMemberFact — реквизит, ресурс, измерение, значение перечисления или
// табличная часть объекта.
type MetadataMemberFact struct {
	// Kind — вид члена: имя XML-элемента (Attribute, TabularSection,
	// Dimension, Resource, EnumValue, AddressingAttribute, Command, ...).
	Kind        string
	NameNorm    string
	NameDisplay string
	Types       []string
	Indexed     bool
	// ParentNorm — нормализованное имя владеющей табличной части; пусто у
	// членов верхнего уровня.
	ParentNorm string
}

// ObjectCommandFact — команда объекта или общая команда.
type ObjectCommandFact struct {
	NameNorm    string
	NameDisplay string
}

// FormDeclFact — форма, объявленная владельцем: аспект form_declaration
// (архитектура §15). Key — стабильный идентификатор формы (см. formKey*),
// одинаковый у FormDeclFact и соответствующего FormStructureFact: это и есть
// проверка «одна identity, а не два объекта».
type FormDeclFact struct {
	Key         string
	NameNorm    string
	NameDisplay string
}

// FormStructureFact — структура формы: аспект form_structure.
type FormStructureFact struct {
	Key      string
	Elements []FormElementFact
	Commands []FormCommandFact
	Handlers []HandlerBindingFact
}

// FormElementFact — элемент формы.
type FormElementFact struct {
	NameNorm    string
	NameDisplay string
	// EType — вид элемента: имя XML-элемента (Field, Table, Button,
	// UsualGroup, Pages, ...).
	EType    string
	DataPath string
}

// FormCommandFact — команда формы.
type FormCommandFact struct {
	NameNorm    string
	NameDisplay string
	// ActionNorm — нормализованное имя обработчика действия команды в модуле формы.
	ActionNorm string
}

// HandlerBindingFact — привязка обработчика к событию формы или элемента.
// Source == "" — событие самой формы, иначе нормализованное имя элемента.
type HandlerBindingFact struct {
	Source          string
	Event           string
	HandlerNameNorm string
	HandlerDisplay  string
}

// ModuleRegistryFact — свойства общего модуля, прочитанные из его XML, а не
// выведенные по имени (критерий приёмки таска 06).
type ModuleRegistryFact struct {
	Global                    bool
	Server                    bool
	ClientManagedApplication  bool
	ClientOrdinaryApplication bool
	ExternalConnection        bool
	ServerCall                bool
	Privileged                bool
	ReturnValuesReuse         string
}

// DocumentFact — свойства документа, не выражаемые плоской картой Props.
type DocumentFact struct {
	// RegisterRecords — движения, декларированные метаданными документа:
	// полные имена регистров ровно в том виде, в каком они записаны в XML
	// ("AccumulationRegister.ТоварыОрганизаций"), в порядке выгрузки.
	// У документа без движений список пустой, но не отсутствующий: сам факт
	// «движений не объявлено» — это факт, а не пробел в разборе.
	RegisterRecords []string
}

// EventSubscriptionSourceKind — форма, которой источник подписки называет
// объект: конкретный тип, голый вид (весь класс объектов) или ОпределяемыйТип.
type EventSubscriptionSourceKind string

const (
	// SourceKindType — <Source><v8:Type>Конкретный.Тип</v8:Type></Source>:
	// подписка на один конкретный тип объекта.
	SourceKindType EventSubscriptionSourceKind = "type"
	// SourceKindBareKind — <Source><v8:TypeSet>DocumentObject</v8:TypeSet></Source>:
	// голый вид без уточнения имени — подписка на ВЕСЬ класс (каждый документ
	// конфигурации и т.п.). Чтение только <v8:Type> теряет этот случай.
	SourceKindBareKind EventSubscriptionSourceKind = "bare-kind"
	// SourceKindDefinedType — <Source><v8:TypeSet>DefinedType.Имя</v8:TypeSet></Source>:
	// источник — ОпределяемыйТип, раскрывающийся в список типов.
	SourceKindDefinedType EventSubscriptionSourceKind = "defined-type"
)

// EventSubscriptionSourceFact — один источник подписки (их может быть
// несколько: несколько <Type>/<TypeSet> под одним <Source>).
type EventSubscriptionSourceFact struct {
	Kind EventSubscriptionSourceKind
	// Name — имя без namespace-префикса ("cfg:", "d5p1:" и подобных). Для
	// SourceKindDefinedType — имя определяемого типа БЕЗ префикса "DefinedType.".
	Name string
}

// EventSubscriptionFact — подписка на событие.
type EventSubscriptionFact struct {
	NameNorm    string
	NameDisplay string
	Sources     []EventSubscriptionSourceFact
	// Event — код события платформы, как записан в Rights.xml/EventSubscriptions
	// (BeforeWrite, OnWrite, ...).
	Event string
	// HandlerRaw — обработчик как записан в выгрузке: "CommonModule.Х.Процедура".
	HandlerRaw string
}

// ScheduledJobFact — регламентное задание.
type ScheduledJobFact struct {
	NameNorm    string
	NameDisplay string
	// MethodRaw — метод как записан в выгрузке: "CommonModule.Х.Процедура".
	MethodRaw  string
	Use        bool
	Predefined bool
}

// RoleFact — идентичность роли (её собственный XML, без прав).
type RoleFact struct {
	NameNorm    string
	NameDisplay string
}

// RoleRightsFact — права роли из Rights.xml. RoleNameNorm берётся из пути
// файла (Roles/<Имя>/Ext/Rights.xml), а не из содержимого: Rights.xml не
// называет свою роль изнутри.
type RoleRightsFact struct {
	RoleNameNorm    string
	RoleNameDisplay string
	// SetForNewObjects — «устанавливать права для новых объектов»: когда
	// включено, отсутствие объекта в Objects НЕ означает отсутствие доступа
	// (rightsaudit.go — образец семантики, обязательной к сохранению).
	SetForNewObjects bool
	Objects          []RoleRightObjectFact
}

// RoleRightObjectFact — права роли на один объект.
type RoleRightObjectFact struct {
	// ObjectNameRaw — полное имя объекта, как записано в Rights.xml
	// (например "Catalog.Товары", "Task.Х.AddressingAttribute.Y").
	ObjectNameRaw string
	Rights        []RoleRightEntryFact
}

// RoleRightEntryFact — одно право, включая отклонённые (Value == false):
// факты хранят исходное значение целиком, эффективная ИЛИ-логика между
// ролями — дело слоя резолвера/приложения, потребляющего эти факты.
type RoleRightEntryFact struct {
	Name  string
	Value bool
	RLS   []RLSRestrictionFact
}

// RLSRestrictionFact — одно ограничение RLS.
type RLSRestrictionFact struct {
	// Fields — поля, к которым относится ограничение; пусто — ограничение на
	// весь объект.
	Fields    []string
	Condition string
}

// PredefinedItemFact — предопределённый элемент справочника/плана видов
// характеристик и подобных объектов из ".../Ext/Predefined.xml".
type PredefinedItemFact struct {
	// OwnerType/OwnerNameRaw — тип и имя владельца, взятые из пути файла
	// (Predefined.xml не называет владельца изнутри).
	OwnerType    string
	OwnerNameRaw string
	NameNorm     string
	NameDisplay  string
	Code         string
	IsFolder     bool
}

// HTTPServiceFact — HTTP-сервис конфигурации: принимающая сторона сшивки
// HTTP-вызовов между базами (веха В2, D10). Адрес метода в опубликованной
// базе: /<имя публикации>/hs/<RootURL><Template>.
type HTTPServiceFact struct {
	RootURL   string
	Templates []HTTPTemplateFact
}

// HTTPTemplateFact — шаблон URL сервиса: "/v1/orders/{Номер}", "/*".
type HTTPTemplateFact struct {
	NameDisplay string
	Template    string
	Methods     []HTTPMethodFact
}

// HTTPMethodFact — метод шаблона: HTTP-метод (GET, POST, ..., ANY) и имя
// процедуры-обработчика в модуле сервиса.
type HTTPMethodFact struct {
	NameDisplay string
	HTTPMethod  string
	Handler     string
}
