package retrieve

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// URI resource-ссылок retrieve строит В ТОМ ЖЕ формате, что internal/app
// (symbolResourceURI/referencesResourceURI/srcResourceURI, зарегистрированы
// как onec://symbol/..., onec://references/..., onec://src/... — тикет 11,
// cmd/mcp1c/idx_symbol.go:registerSymbolResources) — эти resource templates
// УЖЕ зарегистрированы на сервере и обслуживаются app.SymbolService/
// app.GraphService, retrieve не заводит вторую регистрацию. Формат
// продублирован здесь маленькими функциями (а не импортом app: retrieve не
// зависит от app по направлению зависимостей интерфейсов.md), byte-в-byte
// совместим с ParseSymbolResourceURI/ParseSrcResourceURI/
// ParseReferencesResourceURI пакета app, которые их разбирают.

func symbolResourceURI(project domain.ProjectID, uid string, gen domain.Generation) string {
	return fmt.Sprintf("onec://symbol/%s/%s?gen=%s",
		url.PathEscape(string(project)), url.PathEscape(uid), url.QueryEscape(string(gen)))
}

func referencesResourceURI(project domain.ProjectID, uid string, gen domain.Generation) string {
	return fmt.Sprintf("onec://references/%s/%s?gen=%s",
		url.PathEscape(string(project)), url.PathEscape(uid), url.QueryEscape(string(gen)))
}

func srcResourceURI(project domain.ProjectID, component, relPath, hash string, start, end int) string {
	return fmt.Sprintf("onec://src/%s/%s/%s?hash=%s&start=%d&end=%d",
		url.PathEscape(string(project)), url.PathEscape(component), pathEscapeSegments(relPath),
		url.QueryEscape(hash), start, end)
}

func pathEscapeSegments(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}
