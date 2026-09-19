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
