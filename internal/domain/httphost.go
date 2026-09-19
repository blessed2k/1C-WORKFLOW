package domain

import "strings"

// NormalizeHTTPHost приводит сервер соединения к виду, в котором его пишут
// в маппинге хостов workspace (ADR-039): без схемы, пути, порта и учётных данных, в нижнем регистре.
func NormalizeHTTPHost(raw string) string {
	h := strings.TrimSpace(raw)
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if i := strings.LastIndexByte(h, '@'); i >= 0 {
		h = h[i+1:]
	}
	if strings.HasPrefix(h, "[") {
		if i := strings.IndexByte(h, ']'); i >= 0 {
			return strings.ToLower(h[:i+1])
		}
	}
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// HTTPPathKind: насколько путь HTTP-запроса известен из текста кода.
type HTTPPathKind string

const (
	// HTTPPathStatic: путь известен целиком (или целиком от сегмента hs,
	// если PathAnchored).
	HTTPPathStatic HTTPPathKind = "static"
	// HTTPPathPrefix: известно начало пути, дальше вычисляемая часть;
	// PathSuffix хранит статический конец после неё.
	HTTPPathPrefix HTTPPathKind = "prefix"
	// HTTPPathDynamic: путь не выводится.
	HTTPPathDynamic HTTPPathKind = "dynamic"
)

// Причины, по которым адрес HTTP-вызова не выводится, когда их можно назвать.
const (
	// HTTPDynamicConnectionParam: соединение пришло параметром метода, а
	// запрос в этом методе не собирается.
	HTTPDynamicConnectionParam = "connection-from-parameter"
	// HTTPDynamicRequestParam: запрос пришёл параметром метода.
	HTTPDynamicRequestParam = "request-from-parameter"
)

// HTTPTarget: адрес исходящего HTTP-вызова, как его видит код (веха В2,
// ADR-039). Один тип на парсер, хранилище, сшивку и ответ инструмента.
type HTTPTarget struct {
	// Verb: HTTP-метод (GET, POST, ...); пусто, если вычисляется.
	Verb string `json:"verb,omitempty"`
	// Host: сервер из литерала конструктора HTTPСоединение как написан;
	// HostStatic=false, если сервер вычисляется или соединение пришло извне.
	Host       string `json:"host,omitempty"`
	HostStatic bool   `json:"hostStatic,omitempty"`
	// Path: путь целиком (static) или его известное начало (prefix).
	Path     string       `json:"path,omitempty"`
	PathKind HTTPPathKind `json:"pathKind"`
	// PathSuffix: статический конец пути после вычисляемой части (prefix).
	PathSuffix string `json:"pathSuffix,omitempty"`
	// PathAnchored: Path известен от сегмента /hs/, а начало (имя
	// публикации) вычисляется: СтруктураURI.ПутьНаСервере + "/hs/...".
	PathAnchored bool `json:"pathAnchored,omitempty"`
	// DynamicReason: почему адрес не выводится, если причина известна.
	DynamicReason string `json:"dynamicReason,omitempty"`
}
