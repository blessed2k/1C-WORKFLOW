package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/standards"
)

// registerStandardsResources exposes naming/style and query-language
// reference text as static MCP resources. Unlike registerResources, these do
// not depend on an active configuration source: the text is project-wide
// knowledge, not derived from a specific dump or base, so it is available in
// every mode.
func registerStandardsResources(server *mcp.Server) {
	server.AddResource(&mcp.Resource{
		URI:         "onec://standards/naming",
		Name:        "standards-naming",
		Title:       "Стандарты именования и структуры кода 1С",
		Description: "Конвенции БСП/ИТС v8std: именование, структура модуля, комментарии, обработка ошибок, клиент-сервер.",
		MIMEType:    "text/markdown",
	}, textResourceHandler(standards.Naming))

	server.AddResource(&mcp.Resource{
		URI:         "onec://query-lang/cheatsheet",
		Name:        "query-lang-cheatsheet",
		Title:       "Шпаргалка по языку запросов 1С",
		Description: "Виртуальные таблицы, соединения, индексы, временные таблицы, итоги - общие правила языка запросов; дополняет query_advisor.",
		MIMEType:    "text/markdown",
	}, textResourceHandler(standards.QueryCheatsheet))
}

// textResourceHandler returns a resource handler that always serves the same
// fixed text.
func textResourceHandler(text string) func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	return func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/markdown", Text: text}},
		}, nil
	}
}
