package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

const srExtensionXML = metaBOM + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
	<Configuration uuid="77777777-7777-7777-7777-777777777701">
		<Properties>
			<Name>Доработки</Name>
			<ConfigurationExtensionPurpose>Customization</ConfigurationExtensionPurpose>
		</Properties>
		<ChildObjects/>
	</Configuration>
</MetaDataObject>`

// TestSearchCodeReachesExtensionOfActiveProject: search_code looked through the
// main configuration only, so code living in the project's extensions was never
// found. Driven through a real server: the extension root comes from the
// project manifest, not from the test.
func TestSearchCodeReachesExtensionOfActiveProject(t *testing.T) {
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	root := t.TempDir()
	apWriteProject(t, root, "proj-ext", "А", "01")
	manifest, err := json.Marshal(map[string]any{
		"version": 1, "project": "proj-ext",
		"components": []map[string]any{
			{"id": "cfg", "kind": "configuration", "root": "cfg"},
			{"id": "addon", "kind": "extension", "root": "addon", "appliesTo": "cfg", "applyOrder": 1},
		},
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	metaWriteFile(t, filepath.Join(root, "1c-project.json"), string(manifest))
	metaWriteFile(t, filepath.Join(root, "addon", "Configuration.xml"), srExtensionXML)
	metaWriteFile(t, filepath.Join(root, "addon", "CommonModules", "Расш_Сервис", "Ext", "Module.bsl"),
		metaBOM+"Процедура Расш_Пометить() Экспорт\n\t// меткаРасширения\nКонецПроцедуры\n")

	cs, _ := apConnect(t, ctx, options{projectsRoot: workspaceRoot})
	ogReindex(t, ctx, cs, root)

	isErr, raw := apCall(t, ctx, cs, "search_code", map[string]any{"query": "меткаРасширения"})
	if isErr {
		t.Fatalf("search_code: %s", raw)
	}
	if !strings.Contains(raw, `"component":"addon"`) || !strings.Contains(raw, "Расш_Пометить") {
		t.Fatalf("search_code не нашёл код расширения: %s", raw)
	}

	isErr, raw = apCall(t, ctx, cs, "search_code", map[string]any{"query": "Процедура", "scope": "addon/"})
	if isErr {
		t.Fatalf("search_code scope: %s", raw)
	}
	if strings.Contains(raw, "ПроцедураА") || !strings.Contains(raw, "Расш_Пометить") {
		t.Fatalf("scope=addon/ не сузил поиск до расширения: %s", raw)
	}
}
