// Package source abstracts where configuration data comes from. A ConfigSource
// answers the same questions regardless of whether the data is read from an
// offline XML export (XMLSource) or a live HTTP-service connector (added later).
package source

import "context"

// ConfigSource provides read-only access to a 1C configuration. All methods are
// safe for concurrent use.
type ConfigSource interface {
	// ConfigurationInfo returns top-level configuration properties.
	ConfigurationInfo(ctx context.Context) (*ConfigurationInfo, error)
	// MetadataTree returns the configuration objects grouped by metadata type.
	MetadataTree(ctx context.Context) (*MetadataTree, error)
	// ObjectStructure returns the attributes, tabular sections, forms and
	// commands of one object identified by its metadata type and name.
	ObjectStructure(ctx context.Context, objectType, name string) (*ObjectStructure, error)
	// FormStructure returns the attributes, items, commands and event handlers
	// of a managed form. For a common form pass ownerType "CommonForm", ownerName
	// as the form name and an empty formName.
	FormStructure(ctx context.Context, ownerType, ownerName, formName string) (*FormStructure, error)
	// SearchCode searches BSL module text.
	SearchCode(ctx context.Context, params SearchParams) (*SearchResult, error)
	// Close releases any resources held by the source.
	Close() error
}

// LiveSource is a ConfigSource backed by a running base through the read-only
// HTTP-service connector. It adds query and event-log access, which have no
// offline equivalent.
type LiveSource interface {
	ConfigSource
	// ExecuteQuery runs a SELECT-only 1C query and returns the rows.
	ExecuteQuery(ctx context.Context, params QueryParams) (*QueryResult, error)
	// ValidateQuery compiles a query without executing it.
	ValidateQuery(ctx context.Context, text string) (*ValidateResult, error)
	// EventLog reads the registration log with optional filters.
	EventLog(ctx context.Context, params EventLogParams) (*EventLogResult, error)
	// Subsystem returns the composition of one subsystem.
	Subsystem(ctx context.Context, name string) (*SubsystemInfo, error)
	// Predefined returns the predefined items of a catalog or chart.
	Predefined(ctx context.Context, objectType, name string) (*PredefinedList, error)
	// AnalyzeQuery reports heavy-query anti-patterns for a query text.
	AnalyzeQuery(ctx context.Context, text string) (*QueryAnalysis, error)
}

// ConfigurationInfo holds the headline properties of a configuration.
type ConfigurationInfo struct {
	Name             string         `json:"name" jsonschema:"configuration name"`
	Synonym          string         `json:"synonym,omitempty" jsonschema:"Russian synonym"`
	UUID             string         `json:"uuid,omitempty"`
	Vendor           string         `json:"vendor,omitempty"`
	Version          string         `json:"version,omitempty"`
	ScriptVariant    string         `json:"scriptVariant,omitempty" jsonschema:"script language: Russian or English"`
	DefaultRunMode   string         `json:"defaultRunMode,omitempty"`
	PlatformVersion  string         `json:"platformVersion,omitempty" jsonschema:"1C platform version (live mode)"`
	Mode             string         `json:"mode,omitempty" jsonschema:"infobase mode: file or server (live mode)"`
	IsExtension      bool           `json:"isExtension" jsonschema:"true if this is a configuration extension (CFE)"`
	ExtensionPurpose string         `json:"extensionPurpose,omitempty" jsonschema:"extension purpose when isExtension"`
	NamePrefix       string         `json:"namePrefix,omitempty" jsonschema:"object name prefix for an extension"`
	ObjectCounts     map[string]int `json:"objectCounts" jsonschema:"number of objects per metadata type"`

	// Live mode only. Connector is nil when the connector of the base is older than the
	// one that describes itself.
	Connector       *ConnectorInfo  `json:"connector,omitempty" jsonschema:"version and features of the connector extension installed in this base (live mode)"`
	Extensions      []ExtensionInfo `json:"extensions,omitempty" jsonschema:"configuration extensions installed in the base (live mode)"`
	ExtensionsError string          `json:"extensionsError,omitempty" jsonschema:"why the list of extensions is not available to the connector user"`
}

// ConnectorInfo is what the connector extension of a live base says about itself.
// Bases are upgraded one by one, so the versions differ from base to base.
type ConnectorInfo struct {
	Version  string   `json:"version"`
	Features []string `json:"features,omitempty"`
}

// Has reports whether the connector declares the feature.
func (c *ConnectorInfo) Has(feature string) bool {
	if c == nil {
		return false
	}
	for _, f := range c.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// ExtensionInfo is one configuration extension installed in a live base.
type ExtensionInfo struct {
	Name     string `json:"name"`
	Synonym  string `json:"synonym,omitempty"`
	Version  string `json:"version,omitempty"`
	Active   bool   `json:"active"`
	SafeMode any    `json:"safeMode,omitempty" jsonschema:"true, false or the name of a security profile"`
}

// MetadataTree lists configuration objects grouped by their metadata type.
type MetadataTree struct {
	Configuration string          `json:"configuration"`
	TotalObjects  int             `json:"totalObjects"`
	Groups        []MetadataGroup `json:"groups"`
}

// MetadataGroup is one metadata type and the object names of that type.
type MetadataGroup struct {
	Type    string   `json:"type" jsonschema:"metadata type, e.g. Catalog, Document, CommonModule"`
	Objects []string `json:"objects"`
}

// ObjectStructure describes the internal composition of a metadata object.
type ObjectStructure struct {
	Type            string              `json:"type" jsonschema:"metadata type, e.g. Catalog, Document"`
	Name            string              `json:"name"`
	Synonym         string              `json:"synonym,omitempty"`
	UUID            string              `json:"uuid,omitempty"`
	Attributes      []Field             `json:"attributes,omitempty"`
	TabularSections []TabularSection    `json:"tabularSections,omitempty"`
	Forms           []string            `json:"forms,omitempty"`
	Commands        []string            `json:"commands,omitempty"`
	Other           map[string][]string `json:"other,omitempty" jsonschema:"other child objects grouped by type (templates, enum values, ...)"`
}

// Field is an attribute, dimension or resource of an object or tabular section.
type Field struct {
	Name string   `json:"name"`
	Kind string   `json:"kind,omitempty" jsonschema:"Attribute, Dimension or Resource"`
	Type []string `json:"type,omitempty" jsonschema:"one or more 1C type identifiers"`
}

// TabularSection is a tabular section and its attributes.
type TabularSection struct {
	Name       string  `json:"name"`
	Attributes []Field `json:"attributes,omitempty"`
}

// FormStructure describes a managed form.
type FormStructure struct {
	Name       string          `json:"name"`
	Owner      string          `json:"owner,omitempty" jsonschema:"owning object, e.g. Catalog.Товары or CommonForm"`
	Attributes []FormAttribute `json:"attributes,omitempty"`
	Items      []FormItem      `json:"items,omitempty" jsonschema:"UI elements, flattened in layout order"`
	Commands   []FormCommand   `json:"commands,omitempty"`
	Handlers   []FormHandler   `json:"handlers,omitempty" jsonschema:"event handlers of the form and its items"`
}

// FormAttribute is a form data attribute.
type FormAttribute struct {
	Name    string   `json:"name"`
	Type    []string `json:"type,omitempty"`
	Main    bool     `json:"main,omitempty" jsonschema:"true for the main attribute"`
	Columns []Field  `json:"columns,omitempty" jsonschema:"columns of a value-table attribute"`
}

// FormItem is a UI element of a form.
type FormItem struct {
	Name     string `json:"name"`
	Kind     string `json:"kind" jsonschema:"element type, e.g. InputField, Button, Table, UsualGroup"`
	DataPath string `json:"dataPath,omitempty"`
	Command  string `json:"command,omitempty" jsonschema:"bound command for buttons"`
}

// FormCommand is a form command.
type FormCommand struct {
	Name   string `json:"name"`
	Action string `json:"action,omitempty" jsonschema:"handler procedure the command calls"`
}

// FormHandler binds a form or item event to a module procedure.
type FormHandler struct {
	Source  string `json:"source" jsonschema:"\"Form\" or the item name that owns the event"`
	Event   string `json:"event"`
	Handler string `json:"handler"`
}

// SearchParams configures a code search.
type SearchParams struct {
	Query      string `json:"query" jsonschema:"text or regular expression to search for"`
	Regex      bool   `json:"regex,omitempty" jsonschema:"treat query as a regular expression"`
	IgnoreCase bool   `json:"ignoreCase,omitempty" jsonschema:"case-insensitive search"`
	MaxResults int    `json:"maxResults,omitempty" jsonschema:"default 100"`
	Scope      string `json:"scope,omitempty" jsonschema:"only modules whose path contains this, e.g. CommonModules; a module of an extension is addressed as <component>/<path>, so the component id alone narrows the search to that extension"`
	Total      bool   `json:"total,omitempty" jsonschema:"count every match past the limit (slower)"`
}

// SearchResult holds code-search matches.
type SearchResult struct {
	Query        string        `json:"query"`
	Scope        string        `json:"scope,omitempty" jsonschema:"the path filter this search ran under"`
	Matches      []SearchMatch `json:"matches"`
	Shown        int           `json:"shown" jsonschema:"matches returned"`
	TotalMatches int           `json:"totalMatches,omitempty" jsonschema:"exact number of matches; present when total was requested"`
	Truncated    bool          `json:"truncated" jsonschema:"true if maxResults limited the output"`
	Note         string        `json:"note,omitempty"`
}

// SearchMatch is a single matching line in a module.
type SearchMatch struct {
	File string `json:"file" jsonschema:"module path relative to the export root"`
	// Component is set for a hit outside the main export: the id of the project
	// component (an extension) whose root File is relative to.
	Component string `json:"component,omitempty" jsonschema:"project component (extension) the module belongs to; absent for the main configuration"`
	Line      int    `json:"line"`
	Text      string `json:"text"`
	// Procedure is the enclosing Процедура/Функция. A file:line pair says where
	// a hit is; the procedure name says what it is part of, which is what the
	// caller needs before deciding to open the module.
	Procedure string `json:"procedure,omitempty" jsonschema:"enclosing procedure or function"`
}

// QuerySchema describes an object as seen from the 1C query language: the table
// name to select from, its standard fields, own fields, tabular sections and
// (for registers) virtual tables.
type QuerySchema struct {
	TableName       string         `json:"tableName" jsonschema:"query table name, e.g. Справочник.Контрагенты"`
	ObjectType      string         `json:"objectType"`
	Name            string         `json:"name"`
	StandardFields  []QueryField   `json:"standardFields" jsonschema:"standard fields available in queries (Ссылка, Код, ...)"`
	OwnFields       []QueryField   `json:"ownFields,omitempty" jsonschema:"own attributes, dimensions and resources"`
	TabularSections []QueryTable   `json:"tabularSections,omitempty" jsonschema:"tabular sections, queryable as Объект.ИмяТЧ"`
	VirtualTables   []VirtualTable `json:"virtualTables,omitempty" jsonschema:"register virtual tables with their parameters"`
}

// QueryField is one field exposed to the query language.
type QueryField struct {
	Name string   `json:"name"`
	Role string   `json:"role,omitempty" jsonschema:"Стандартный, Реквизит, Измерение or Ресурс"`
	Type []string `json:"type,omitempty"`
}

// QueryTable is a tabular section and its queryable columns.
type QueryTable struct {
	Name   string       `json:"name"`
	Fields []QueryField `json:"fields,omitempty"`
}

// VirtualTable is a register virtual table (Остатки, Обороты, СрезПоследних, ...).
type VirtualTable struct {
	Name       string   `json:"name" jsonschema:"full virtual table name, e.g. РегистрНакопления.Товары.Остатки"`
	Suffix     string   `json:"suffix" jsonschema:"virtual table suffix, e.g. Остатки"`
	Parameters []string `json:"parameters" jsonschema:"virtual table parameters in order"`
}

// UsageReport lists where a metadata object is used across the configuration.
type UsageReport struct {
	Object  string      `json:"object" jsonschema:"target object, e.g. Справочник.Контрагенты"`
	AsType  []TypeUsage `json:"asType,omitempty" jsonschema:"fields of other objects typed by this object's reference"`
	InRoles []RoleUsage `json:"inRoles,omitempty" jsonschema:"roles that grant rights on this object"`
	// Templates carry the usages the type and rights scans cannot see: a report's
	// data composition schema keeps its query as text, and an exchange plan keeps
	// its registration rules the same way.
	InTemplates   []TemplateUsage `json:"inTemplates,omitempty" jsonschema:"data composition schemas and other text templates mentioning the object"`
	TemplatesNote string          `json:"templatesNote,omitempty" jsonschema:"set when templates were not scanned"`
	Total         int             `json:"total"`
}

// TypeUsage is one field whose type is the target object's reference.
type TypeUsage struct {
	Object string `json:"object" jsonschema:"owning object, e.g. РегистрСведений.КотировкиАкций"`
	Field  string `json:"field"`
	Kind   string `json:"kind" jsonschema:"Реквизит, Измерение, Ресурс or Реквизит ТЧ"`
}

// RoleUsage is one role that grants rights on the target object.
type RoleUsage struct {
	Role   string   `json:"role"`
	Rights []string `json:"rights" jsonschema:"granted rights (value=true), e.g. Read, Insert, Update"`
}

// DependencyReport lists the shortest chains of links between two objects.
type DependencyReport struct {
	From     string           `json:"from" jsonschema:"start object, e.g. Справочник.Товары"`
	To       string           `json:"to" jsonschema:"target object, e.g. РегистрНакопления.ТоварыНаСкладах"`
	MaxDepth int              `json:"maxDepth" jsonschema:"how many steps were allowed"`
	Paths    []DependencyPath `json:"paths,omitempty" jsonschema:"paths of the shortest length, in graph order"`
	Found    int              `json:"found"`
	Note     string           `json:"note,omitempty" jsonschema:"why the answer is empty or truncated"`
}

// DependencyPath is one chain of links from the start object to the target.
type DependencyPath struct {
	Length int              `json:"length" jsonschema:"number of steps"`
	Steps  []DependencyStep `json:"steps"`
}

// DependencyStep is one link of a path.
type DependencyStep struct {
	From string `json:"from"`
	To   string `json:"to"`
	Via  string `json:"via" jsonschema:"what links the two: a typed field or document movements"`
}

// QueryAdvice holds static analysis of a 1C query: anti-pattern findings with
// concrete rewrites and unindexed-filter hints derived from object metadata.
type QueryAdvice struct {
	Tables   []string     `json:"tables,omitempty" jsonschema:"query sources recognised as configuration objects"`
	Warnings []AdviceItem `json:"warnings"`
	Count    int          `json:"count"`
}

// AdviceItem is one finding.
type AdviceItem struct {
	Code       string `json:"code" jsonschema:"finding code, e.g. JoinWithSubquery"`
	Severity   string `json:"severity" jsonschema:"high, medium or low"`
	Message    string `json:"message" jsonschema:"what is wrong"`
	Suggestion string `json:"suggestion" jsonschema:"how to rewrite it"`
	Field      string `json:"field,omitempty" jsonschema:"related field, when applicable"`
}

// QueryParams configures a live query execution.
type QueryParams struct {
	Text   string         `json:"text" jsonschema:"1C query text; must be a SELECT (ВЫБРАТЬ) query"`
	Params map[string]any `json:"params,omitempty" jsonschema:"query parameters by name. A date is a string 2026-09-01 or 2026-09-01T00:00:00; a reference is the object a refs=true query returned: {type, ref}; an enum value is {type, value}; a list for В (&Список) is an array of these"`
	Limit  int            `json:"limit,omitempty" jsonschema:"max rows to return (default 100)"`
	Refs   bool           `json:"refs,omitempty" jsonschema:"true: a reference in a row comes as {presentation, type, ref} (an enum value as {presentation, type, value}) instead of its presentation, so it can be passed back in params; filter the next query by the reference, not by number or name"`
}

// QueryResult holds the rows returned by a query.
type QueryResult struct {
	Columns   []string         `json:"columns"`
	Rows      []map[string]any `json:"rows"`
	Count     int              `json:"count"`
	Truncated bool             `json:"truncated,omitempty" jsonschema:"true if the row limit was hit"`
	// DurationMS is how long the query ran in the base, as measured by the connector;
	// zero when the connector is older and does not report it.
	DurationMS int `json:"durationMs,omitempty" jsonschema:"how long the query ran in the base, milliseconds"`
}

// ValidateResult reports whether a query compiles.
type ValidateResult struct {
	Valid bool   `json:"valid"`
	Error string `json:"error,omitempty"`
}

// EventLogParams filters the registration log.
type EventLogParams struct {
	StartDate string `json:"startDate,omitempty" jsonschema:"lower bound, ISO 8601"`
	EndDate   string `json:"endDate,omitempty" jsonschema:"upper bound, ISO 8601"`
	Level     string `json:"level,omitempty" jsonschema:"Error, Warning, Information or Note"`
	User      string `json:"user,omitempty" jsonschema:"infobase user name"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max entries (default 100, at most 500)"`

	// The filters below need a connector that reports the filters it applied; with an
	// older one the call fails instead of returning the whole window.
	Events            []string       `json:"events,omitempty" jsonschema:"event names as the log stores them: eventId of an entry (_$Data$_.New, _$Data$_.Post, _$Session$_.Start) or the name of an applied event"`
	Metadata          []string       `json:"metadata,omitempty" jsonschema:"full names of metadata objects, e.g. Документ.РеализацияТоваровУслуг"`
	Sessions          []int          `json:"sessions,omitempty" jsonschema:"session numbers"`
	Applications      []string       `json:"applications,omitempty" jsonschema:"application ids as applicationId of an entry: 1CV8C, BackgroundJob, HTTPServiceConnection"`
	Computer          string         `json:"computer,omitempty"`
	Data              map[string]any `json:"data,omitempty" jsonschema:"the object the entry is about, as a reference {type, ref} returned by execute_query with refs=true"`
	DataPresentation  string         `json:"dataPresentation,omitempty" jsonschema:"exact presentation of the data, as dataPresentation of an entry"`
	Comment           string         `json:"comment,omitempty" jsonschema:"exact comment text"`
	TransactionStatus string         `json:"transactionStatus,omitempty" jsonschema:"Committed, RolledBack, Unfinished or NotApplicable"`
	Transaction       string         `json:"transaction,omitempty" jsonschema:"transaction id, as transaction of an entry"`
	Order             string         `json:"order,omitempty" jsonschema:"desc: the last entries of the window, newest first. asc: the first entries of the window, oldest first; works when at most 5000 entries match. Omitted: the last entries, oldest first"`
	Offset            int            `json:"offset,omitempty" jsonschema:"entries to skip in the chosen order, for paging"`
	MaxComment        int            `json:"maxComment,omitempty" jsonschema:"cut each comment to this many characters (default 4000; an error comment carries a whole call stack)"`
}

// EventLogResult holds registration-log entries.
type EventLogResult struct {
	Entries   []EventLogEntry `json:"entries"`
	Count     int             `json:"count"`
	Truncated bool            `json:"truncated,omitempty"`
}

// EventLogEntry is one registration-log record.
type EventLogEntry struct {
	Date     string `json:"date"`
	Level    string `json:"level"`
	User     string `json:"user,omitempty"`
	Event    string `json:"event,omitempty"`
	Comment  string `json:"comment,omitempty"`
	Metadata string `json:"metadata,omitempty"`

	// Filled by a connector that reads the full set of columns; empty with an older one.
	EventID           string `json:"eventId,omitempty" jsonschema:"system name of the event, usable in the events filter"`
	Computer          string `json:"computer,omitempty"`
	Application       string `json:"application,omitempty"`
	ApplicationID     string `json:"applicationId,omitempty" jsonschema:"usable in the applications filter"`
	Session           int    `json:"session,omitempty"`
	Connection        int    `json:"connection,omitempty"`
	MetadataID        string `json:"metadataId,omitempty" jsonschema:"full name of the metadata object, usable in the metadata filter"`
	Data              any    `json:"data,omitempty" jsonschema:"the object the entry is about: a reference {presentation, type, ref} or a presentation"`
	DataPresentation  string `json:"dataPresentation,omitempty"`
	TransactionStatus string `json:"transactionStatus,omitempty"`
	Transaction       string `json:"transaction,omitempty"`
}

// SubsystemInfo describes a subsystem's composition.
type SubsystemInfo struct {
	Name       string   `json:"name"`
	Synonym    string   `json:"synonym,omitempty"`
	Subsystems []string `json:"subsystems,omitempty" jsonschema:"names of child subsystems"`
	Content    []string `json:"content,omitempty" jsonschema:"full names of objects in the subsystem"`
}

// PredefinedList holds the predefined items of an object.
type PredefinedList struct {
	Items     []PredefinedItem `json:"items"`
	Count     int              `json:"count"`
	Truncated bool             `json:"truncated,omitempty" jsonschema:"true if the connector cut the list at its ceiling"`
}

// PredefinedItem is one predefined element.
type PredefinedItem struct {
	Name string `json:"name" jsonschema:"predefined data name"`
	Ref  string `json:"ref,omitempty" jsonschema:"reference presentation"`
}

// QueryAnalysis holds heavy-query anti-pattern warnings.
type QueryAnalysis struct {
	Warnings []QueryWarning `json:"warnings"`
	Count    int            `json:"count"`
}

// QueryWarning is one anti-pattern finding.
type QueryWarning struct {
	Code     string `json:"code"`
	Severity string `json:"severity" jsonschema:"high, medium or low"`
	Message  string `json:"message"`
}
