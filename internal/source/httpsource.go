package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// maxResponseBytes caps how much of a connector response is read.
const maxResponseBytes = 128 << 20 // 128 MiB

// HTTPSource talks to the MCPService HTTP-service of the connector extension (see connector/)
// (RootURL mcp-1c) and adapts its protocol to the normalized ConfigSource /
// LiveSource types, so live and offline tools return the same shapes. Module
// source code and form layout are not available live; those methods return an
// error pointing at offline (--dump) mode.
type HTTPSource struct {
	base   string
	user   string
	pass   string
	client *http.Client
}

// NewHTTPSource returns a source talking to the connector at base (the published
// HTTP-service root, e.g. http://host/base/hs/mcp-1c).
func NewHTTPSource(base, user, pass string) *HTTPSource {
	return &HTTPSource{
		base:   strings.TrimRight(base, "/"),
		user:   user,
		pass:   pass,
		client: &http.Client{Timeout: 300 * time.Second},
	}
}

// Close implements ConfigSource.
func (s *HTTPSource) Close() error { return nil }

func (s *HTTPSource) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+path, nil)
	if err != nil {
		return err
	}
	return s.do(req, out)
}

func (s *HTTPSource) post(ctx context.Context, path string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return s.do(req, out)
}

func (s *HTTPSource) do(req *http.Request, out any) error {
	if s.user != "" {
		req.SetBasicAuth(s.user, s.pass)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("connector request %s: %w", req.URL.Path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("read %s response: %w", req.URL.Path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("connector %s: HTTP %d: %s", req.URL.Path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode %s response: %w", req.URL.Path, err)
		}
	}
	return nil
}

// ConfigurationInfo implements ConfigSource via GET /configuration. The connector
// does not return per-type object counts, so ObjectCounts is left empty in live
// mode (use get_metadata_tree for the object list).
func (s *HTTPSource) ConfigurationInfo(ctx context.Context) (*ConfigurationInfo, error) {
	var w struct {
		Name            string `json:"name"`
		Synonym         string `json:"synonym"`
		Version         string `json:"version"`
		Vendor          string `json:"vendor"`
		PlatformVersion string `json:"platform_version"`
		Mode            string `json:"mode"`
	}
	if err := s.get(ctx, "/configuration", &w); err != nil {
		return nil, err
	}
	return &ConfigurationInfo{
		Name:            w.Name,
		Synonym:         w.Synonym,
		Version:         w.Version,
		Vendor:          w.Vendor,
		PlatformVersion: w.PlatformVersion,
		Mode:            w.Mode,
		ObjectCounts:    map[string]int{},
	}, nil
}

// ruCollectionToType maps the Russian collection keys returned by /metadata to
// English metadata type names.
var ruCollectionToType = map[string]string{
	"Справочники":             "Catalog",
	"Документы":               "Document",
	"Перечисления":            "Enum",
	"Обработки":               "DataProcessor",
	"Отчеты":                  "Report",
	"РегистрыСведений":        "InformationRegister",
	"РегистрыНакопления":      "AccumulationRegister",
	"РегистрыБухгалтерии":     "AccountingRegister",
	"РегистрыРасчета":         "CalculationRegister",
	"ПланыСчетов":             "ChartOfAccounts",
	"ПланыВидовХарактеристик": "ChartOfCharacteristicTypes",
	"ПланыВидовРасчета":       "ChartOfCalculationTypes",
	"ПланыОбмена":             "ExchangePlan",
	"БизнесПроцессы":          "BusinessProcess",
	"Задачи":                  "Task",
	"ЖурналыДокументов":       "DocumentJournal",
	"Константы":               "Constant",
	"ОбщиеМодули":             "CommonModule",
	"ОбщиеФормы":              "CommonForm",
	"ОбщиеКоманды":            "CommonCommand",
	"ОбщиеМакеты":             "CommonTemplate",
	"Роли":                    "Role",
	"Подсистемы":              "Subsystem",
	"РегулярныеЗадания":       "ScheduledJob",
	"ВебСервисы":              "WebService",
	"HTTPСервисы":             "HTTPService",
	// Типы, которые есть в XML-выгрузке: без них check_sync не сравнивает их вовсе
	// и отвечает «расхождений нет», просто не заглянув в эти коллекции.
	"ГруппыКоманд":                 "CommandGroup",
	"ОбщиеРеквизиты":               "CommonAttribute",
	"ОбщиеКартинки":                "CommonPicture",
	"ОпределяемыеТипы":             "DefinedType",
	"НумераторыДокументов":         "DocumentNumerator",
	"ПодпискиНаСобытия":            "EventSubscription",
	"КритерииОтбора":               "FilterCriterion",
	"ФункциональныеОпции":          "FunctionalOption",
	"ПараметрыФункциональныхОпций": "FunctionalOptionsParameter",
	"СервисыИнтеграции":            "IntegrationService",
	"Языки":                        "Language",
	"ПараметрыСеанса":              "SessionParameter",
	"ХранилищаНастроек":            "SettingsStorage",
	"ЭлементыСтиля":                "StyleItem",
	"WSСсылки":                     "WSReference",
	"ПакетыXDTO":                   "XDTOPackage",
}

// MetadataTree implements ConfigSource via GET /metadata, mapping the Russian
// collection keys to English type names.
func (s *HTTPSource) MetadataTree(ctx context.Context) (*MetadataTree, error) {
	var raw map[string][]string
	if err := s.get(ctx, "/metadata", &raw); err != nil {
		return nil, err
	}

	groups := make([]MetadataGroup, 0, len(raw))
	total := 0
	for ruKey, names := range raw {
		if len(names) == 0 {
			continue
		}
		typ := ruCollectionToType[ruKey]
		if typ == "" {
			typ = ruKey
		}
		sorted := append([]string(nil), names...)
		sort.Strings(sorted)
		groups = append(groups, MetadataGroup{Type: typ, Objects: sorted})
		total += len(names)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Type < groups[j].Type })

	info, err := s.ConfigurationInfo(ctx)
	name := ""
	if err == nil {
		name = info.Name
	}
	return &MetadataTree{Configuration: name, TotalObjects: total, Groups: groups}, nil
}

// flexTypes decodes a JSON field that may be either a single string or an array
// of strings (older vs 0.5.0+ connector) into a []string.
type flexTypes []string

func (t *flexTypes) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*t = nil
		return nil
	}
	if data[0] == '[' {
		var arr []string
		if err := json.Unmarshal(data, &arr); err != nil {
			return err
		}
		*t = arr
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s == "" {
		*t = nil
	} else {
		*t = []string{s}
	}
	return nil
}

// wireField accepts the connector's field description; type may be a single
// string (older connector) or an array (0.5.0+), handled by flexTypes.
type wireField struct {
	Name    string    `json:"name"`
	Synonym string    `json:"synonym"`
	Type    flexTypes `json:"type"`
}

func (f wireField) field(kind string) Field {
	return Field{Name: f.Name, Kind: kind, Type: []string(f.Type)}
}

// ObjectStructure implements ConfigSource via GET /object/{type}/{name}.
func (s *HTTPSource) ObjectStructure(ctx context.Context, objectType, name string) (*ObjectStructure, error) {
	if objectType == "" || name == "" {
		return nil, fmt.Errorf("object type and name are required")
	}
	path := "/object/" + url.PathEscape(objectType) + "/" + url.PathEscape(name)

	var w struct {
		Name         string      `json:"name"`
		Synonym      string      `json:"synonym"`
		Attributes   []wireField `json:"attributes"`
		TabularParts []struct {
			Name       string      `json:"name"`
			Attributes []wireField `json:"attributes"`
		} `json:"tabularParts"`
		Dimensions []wireField `json:"dimensions"`
		Resources  []wireField `json:"resources"`
		Values     []struct {
			Name string `json:"name"`
		} `json:"values"`
		Forms    []string `json:"forms"`
		Commands []string `json:"commands"`
	}
	if err := s.get(ctx, path, &w); err != nil {
		return nil, err
	}

	out := &ObjectStructure{Type: objectType, Name: w.Name, Synonym: w.Synonym}
	for _, a := range w.Attributes {
		out.Attributes = append(out.Attributes, a.field("Attribute"))
	}
	for _, d := range w.Dimensions {
		out.Attributes = append(out.Attributes, d.field("Dimension"))
	}
	for _, r := range w.Resources {
		out.Attributes = append(out.Attributes, r.field("Resource"))
	}
	for _, ts := range w.TabularParts {
		sec := TabularSection{Name: ts.Name}
		for _, a := range ts.Attributes {
			sec.Attributes = append(sec.Attributes, a.field("Attribute"))
		}
		out.TabularSections = append(out.TabularSections, sec)
	}
	out.Forms = w.Forms
	out.Commands = w.Commands
	if len(w.Values) > 0 {
		names := make([]string, len(w.Values))
		for i, v := range w.Values {
			names[i] = v.Name
		}
		out.Other = map[string][]string{"values": names}
	}
	return out, nil
}

// FormStructure implements ConfigSource; live form layout is unreliable (form
// items are not exposed at runtime), so it points at offline mode.
func (s *HTTPSource) FormStructure(_ context.Context, _, _, _ string) (*FormStructure, error) {
	return nil, errors.New("form structure is available only in offline mode (--dump); the live connector cannot enumerate form items at runtime")
}

// SearchCode implements ConfigSource; there is no live equivalent.
func (s *HTTPSource) SearchCode(_ context.Context, _ SearchParams) (*SearchResult, error) {
	return nil, errors.New("code search is available only in offline mode (--dump); the live connector does not expose module source")
}

// ExecuteQuery implements LiveSource via POST /query. Rows come back positional
// and are re-keyed by column. The SELECT guard here is defense in depth.
func (s *HTTPSource) ExecuteQuery(ctx context.Context, params QueryParams) (*QueryResult, error) {
	if err := ensureSelect(params.Text); err != nil {
		return nil, err
	}
	body := map[string]any{"query": params.Text}
	if len(params.Params) > 0 {
		body["parameters"] = params.Params
	}
	if params.Limit > 0 {
		body["limit"] = params.Limit
	}

	var w struct {
		Columns   []string `json:"columns"`
		Rows      [][]any  `json:"rows"`
		Total     int      `json:"total"`
		Truncated bool     `json:"truncated"`
	}
	if err := s.post(ctx, "/query", body, &w); err != nil {
		return nil, err
	}

	rows := make([]map[string]any, 0, len(w.Rows))
	for _, r := range w.Rows {
		m := make(map[string]any, len(w.Columns))
		for i, col := range w.Columns {
			if i < len(r) {
				m[col] = r[i]
			}
		}
		rows = append(rows, m)
	}
	return &QueryResult{Columns: w.Columns, Rows: rows, Count: w.Total, Truncated: w.Truncated}, nil
}

// ValidateQuery implements LiveSource via POST /validate-query.
func (s *HTTPSource) ValidateQuery(ctx context.Context, text string) (*ValidateResult, error) {
	var w struct {
		Valid  bool     `json:"valid"`
		Errors []string `json:"errors"`
	}
	if err := s.post(ctx, "/validate-query", map[string]string{"query": text}, &w); err != nil {
		return nil, err
	}
	return &ValidateResult{Valid: w.Valid, Error: strings.Join(w.Errors, "; ")}, nil
}

// eventLevelToRussian maps English log levels to the Russian names the connector
// expects; unknown values pass through unchanged.
var eventLevelToRussian = map[string]string{
	"Error":       "Ошибка",
	"Warning":     "Предупреждение",
	"Information": "Информация",
	"Note":        "Примечание",
}

// EventLog implements LiveSource via POST /eventlog.
func (s *HTTPSource) EventLog(ctx context.Context, params EventLogParams) (*EventLogResult, error) {
	body := map[string]any{}
	if params.StartDate != "" {
		body["start_date"] = params.StartDate
	}
	if params.EndDate != "" {
		body["end_date"] = params.EndDate
	}
	if params.Level != "" {
		lvl := eventLevelToRussian[params.Level]
		if lvl == "" {
			lvl = params.Level
		}
		body["level"] = lvl
	}
	if params.User != "" {
		body["user"] = params.User
	}
	if params.Limit > 0 {
		body["limit"] = params.Limit
	}

	var w struct {
		Events []struct {
			Date     string `json:"date"`
			Level    string `json:"level"`
			Event    string `json:"event"`
			User     string `json:"user"`
			Comment  string `json:"comment"`
			Metadata string `json:"metadata"`
		} `json:"events"`
		Total     int  `json:"total"`
		Truncated bool `json:"truncated"`
	}
	if err := s.post(ctx, "/eventlog", body, &w); err != nil {
		return nil, err
	}

	entries := make([]EventLogEntry, len(w.Events))
	for i, e := range w.Events {
		entries[i] = EventLogEntry{
			Date:     e.Date,
			Level:    e.Level,
			User:     e.User,
			Event:    e.Event,
			Comment:  e.Comment,
			Metadata: e.Metadata,
		}
	}
	// Обрезку определяет коннектор: он знает и свой потолок, и то, была ли следующая запись.
	return &EventLogResult{Entries: entries, Count: w.Total, Truncated: w.Truncated}, nil
}

// Subsystem implements LiveSource via GET /subsystem/{name}.
func (s *HTTPSource) Subsystem(ctx context.Context, name string) (*SubsystemInfo, error) {
	if name == "" {
		return nil, fmt.Errorf("subsystem name is required")
	}
	var out SubsystemInfo
	if err := s.get(ctx, "/subsystem/"+url.PathEscape(name), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Predefined implements LiveSource via GET /predefined/{type}/{name}.
func (s *HTTPSource) Predefined(ctx context.Context, objectType, name string) (*PredefinedList, error) {
	if objectType == "" || name == "" {
		return nil, fmt.Errorf("object type and name are required")
	}
	path := "/predefined/" + url.PathEscape(objectType) + "/" + url.PathEscape(name)
	var w struct {
		Items     []PredefinedItem `json:"items"`
		Total     int              `json:"total"`
		Truncated bool             `json:"truncated"`
	}
	if err := s.get(ctx, path, &w); err != nil {
		return nil, err
	}
	return &PredefinedList{Items: w.Items, Count: w.Total, Truncated: w.Truncated}, nil
}

// AnalyzeQuery implements LiveSource. The heavy-query rules are pure text
// analysis, so they run locally through the same code as the offline advisor
// instead of a connector endpoint: one implementation, no round trip. The index
// checks stay offline-only — they read the Indexing flag, which lives in the XML
// export and is not exposed at runtime.
func (s *HTTPSource) AnalyzeQuery(_ context.Context, text string) (*QueryAnalysis, error) {
	adv, _, _ := adviseQueryText(text)
	out := &QueryAnalysis{Warnings: make([]QueryWarning, 0, len(adv.Warnings))}
	for _, w := range adv.Warnings {
		message := w.Message
		if w.Suggestion != "" {
			message += " " + w.Suggestion
		}
		out.Warnings = append(out.Warnings, QueryWarning{Code: w.Code, Severity: w.Severity, Message: message})
	}
	out.Count = len(out.Warnings)
	return out, nil
}

// ensureSelect rejects anything that is not a SELECT (ВЫБРАТЬ) query, ignoring
// leading comments and whitespace.
func ensureSelect(text string) error {
	t := strings.TrimSpace(stripLeadingComments(text))
	up := strings.ToUpper(t)
	if startsWithKeyword(up, "ВЫБРАТЬ") || startsWithKeyword(up, "SELECT") {
		return nil
	}
	return errors.New("only SELECT (ВЫБРАТЬ) queries are allowed")
}

// startsWithKeyword reports whether text starts with the keyword as a whole word:
// a bare prefix check also accepts identifiers such as ВЫБРАТЬЧТОУГОДНО.
func startsWithKeyword(text, keyword string) bool {
	if !strings.HasPrefix(text, keyword) {
		return false
	}
	rest := text[len(keyword):]
	if rest == "" {
		return true
	}
	r := rune(rest[0])
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// stripLeadingComments drops leading blank and // comment lines.
func stripLeadingComments(text string) string {
	lines := strings.Split(text, "\n")
	for len(lines) > 0 {
		l := strings.TrimSpace(lines[0])
		if l == "" || strings.HasPrefix(l, "//") {
			lines = lines[1:]
			continue
		}
		break
	}
	return strings.Join(lines, "\n")
}
