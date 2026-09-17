package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// registerResources exposes configuration data as MCP resources: a static
// metadata tree and a per-object template. Both read through provide(), so they
// follow the active offline/live source.
func registerResources(server *mcp.Server, provide func() source.ConfigSource) {
	server.AddResource(&mcp.Resource{
		URI:         "onec://metadata",
		Name:        "metadata-tree",
		Title:       "Дерево метаданных 1С",
		Description: "Объекты конфигурации, сгруппированные по типу метаданных.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		src := provide()
		if src == nil {
			return nil, errNoSource
		}
		tree, err := src.MetadataTree(ctx)
		if err != nil {
			return nil, err
		}
		return jsonResource(req.Params.URI, tree)
	})

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "onec://object/{type}/{name}",
		Name:        "object-structure",
		Title:       "Структура объекта 1С",
		Description: "Реквизиты, табличные части, формы и команды объекта. Пример: onec://object/Catalog/Контрагенты",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		src := provide()
		if src == nil {
			return nil, errNoSource
		}
		objType, name, ok := parseObjectURI(req.Params.URI)
		if !ok {
			return nil, fmt.Errorf("invalid object URI %q; expected onec://object/{type}/{name}", req.Params.URI)
		}
		obj, err := src.ObjectStructure(ctx, objType, name)
		if err != nil {
			return nil, err
		}
		return jsonResource(req.Params.URI, obj)
	})
}

// parseObjectURI splits "onec://object/Catalog/Контрагенты" into type and name.
func parseObjectURI(uri string) (objType, name string, ok bool) {
	rest, found := strings.CutPrefix(uri, "onec://object/")
	if !found {
		return "", "", false
	}
	objType, name, found = strings.Cut(rest, "/")
	if !found || objType == "" || name == "" {
		return "", "", false
	}
	// RFC 6570 template expansion percent-encodes non-ASCII, so a Cyrillic
	// object name arrives percent-encoded (e.g. %D0%A2...); decode it back.
	if d, err := url.PathUnescape(objType); err == nil {
		objType = d
	}
	if d, err := url.PathUnescape(name); err == nil {
		name = d
	}
	return objType, name, true
}

// jsonResource marshals v as an application/json resource content.
func jsonResource(uri string, v any) (*mcp.ReadResourceResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "application/json", Text: string(data)}},
	}, nil
}

// completionHandler returns a CompletionHandler that suggests real metadata
// names (for the object resource template) and argument values (for prompts).
// This closes the main pain point of long Cyrillic metadata names.
func completionHandler(provide func() source.ConfigSource) func(context.Context, *mcp.CompleteRequest) (*mcp.CompleteResult, error) {
	return func(ctx context.Context, req *mcp.CompleteRequest) (*mcp.CompleteResult, error) {
		p := req.Params
		value := p.Argument.Value
		var candidates []string

		switch {
		case p.Ref != nil && p.Ref.Type == "ref/resource":
			candidates = completeObjectURI(ctx, provide, p)
		}

		// Match all, then cap at the protocol's 100-item limit, reporting the
		// true total and whether more matches were dropped.
		const maxCompletionValues = 100
		matched := filterPrefix(candidates, value, len(candidates))
		total := len(matched)
		hasMore := false
		if total > maxCompletionValues {
			matched = matched[:maxCompletionValues]
			hasMore = true
		}
		return &mcp.CompleteResult{Completion: mcp.CompletionResultDetails{
			Values: matched, Total: total, HasMore: hasMore,
		}}, nil
	}
}

// completeObjectURI suggests metadata types and object names for the
// onec://object/{type}/{name} template.
func completeObjectURI(ctx context.Context, provide func() source.ConfigSource, p *mcp.CompleteParams) []string {
	src := provide()
	if src == nil {
		return nil
	}
	tree, err := src.MetadataTree(ctx)
	if err != nil {
		return nil
	}
	switch p.Argument.Name {
	case "type":
		var types []string
		for _, g := range tree.Groups {
			types = append(types, g.Type)
		}
		return types
	case "name":
		// The already-entered type, if any, narrows the object list.
		wantType := ""
		if p.Context != nil {
			wantType = p.Context.Arguments["type"]
		}
		var names []string
		for _, g := range tree.Groups {
			if wantType != "" && g.Type != wantType {
				continue
			}
			names = append(names, g.Objects...)
		}
		return names
	}
	return nil
}

// filterPrefix keeps values that contain sub (case-insensitive), capped at limit.
func filterPrefix(values []string, sub string, limit int) []string {
	sub = strings.ToLower(strings.TrimSpace(sub))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if sub == "" || strings.Contains(strings.ToLower(v), sub) {
			out = append(out, v)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}
