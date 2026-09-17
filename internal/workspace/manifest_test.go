package workspace_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// bom — выгрузки 1С пишутся с BOM: фикстуры несут его, иначе ветка чтения BOM
// в обычном прогоне не исполняется.
const bom = "\ufeff"

// конфигурацияXML возвращает Configuration.xml выгрузки. Непустой purpose
// делает выгрузку расширением — ровно так его определяет платформа и
// существующий internal/source/xmlsource.go.

func конфигурацияXML(name, purpose string) string {
	расширение := ""
	if purpose != "" {
		расширение = "\n\t\t\t<ConfigurationExtensionPurpose>" + purpose + "</ConfigurationExtensionPurpose>"
	}
	// InternalInfo идёт перед Properties и содержит собственные вложенные
	// элементы — ровно так же, как в настоящей выгрузке.
	return bom + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
	<Configuration uuid="0d3a94e9-6b9d-4b5a-9c1e-2f4a1d1b0001">
		<InternalInfo>
			<xr:ContainedObject>
				<xr:ClassId>9cd510cd-abfc-11d4-9434-004095e12fc7</xr:ClassId>
				<xr:ObjectId>1a3c1c1f-0b2e-4d5a-9f2b-7d6d1a2b3c4d</xr:ObjectId>
			</xr:ContainedObject>
			<xr:GeneratedType name="ConfigurationObject.Чужое" category="Object">
				<xr:TypeId>2b4d2d20-1c3f-4e6b-a03c-8e7e2b3c4d5e</xr:TypeId>
			</xr:GeneratedType>
		</InternalInfo>
		<Properties>
			<Name>` + name + `</Name>` + расширение + `
		</Properties>
		<ChildObjects/>
	</Configuration>
</MetaDataObject>`
}

// внешнийОбъектXML возвращает корневой XML разобранной обработки или отчёта
// (kind — ExternalDataProcessor или ExternalReport).
func внешнийОбъектXML(kind, name string) string {
	return bom + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.17">
	<` + kind + ` uuid="0d3a94e9-6b9d-4b5a-9c1e-2f4a1d1b0002">
		<InternalInfo>
			<xr:GeneratedType name="` + kind + `Object.Чужое" category="Object">
				<xr:TypeId>3c5e3e31-2d40-4f7c-b14d-9f8f3c4d5e6f</xr:TypeId>
			</xr:GeneratedType>
		</InternalInfo>
		<Properties>
			<Name>` + name + `</Name>
		</Properties>
	</` + kind + `>
</MetaDataObject>`
}

// записать создаёт файл вместе с недостающими каталогами.
func записать(t *testing.T, path, содержимое string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("создание каталога для %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(содержимое), 0o644); err != nil {
		t.Fatalf("запись %s: %v", path, err)
	}
}

// проектИзАрхитектуры раскладывает на диске проект из примера §9 архитектуры и
// возвращает его корень.
func проектИзАрхитектуры(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	записать(t, filepath.Join(root, "dumps", "ut_main", "Configuration.xml"), конфигурацияXML("УправлениеТорговлей", ""))
	записать(t, filepath.Join(root, "dumps", "ext_fix", "Configuration.xml"), конфигурацияXML("ИсправлениеОшибок", "Patch"))
	записать(t, filepath.Join(root, "ai-assist", "src", "Configuration.xml"), конфигурацияXML("ПомощникИИ", "Customization"))
	записать(t, filepath.Join(root, "tools", "ЗагрузкаЦен", "src", "ЗагрузкаЦен.xml"), внешнийОбъектXML("ExternalDataProcessor", "ЗагрузкаЦен"))
	записать(t, filepath.Join(root, "tests", "yaxunit", "ТестыОбмена.bsl"), "Процедура Тест() КонецПроцедуры")

	записать(t, filepath.Join(root, workspace.ManifestFileName), `{
  "version": 1,
  "project": "ut-main",
  "displayName": "УТ 11.5 (доработки)",
  "components": [
    { "id": "cfg",      "kind": "configuration",          "root": "dumps/ut_main" },
    { "id": "ext-fix",  "kind": "extension",              "root": "dumps/ext_fix",
      "appliesTo": "cfg", "applyOrder": 1 },
    { "id": "ext-ai",   "kind": "extension",              "root": "ai-assist/src",
      "appliesTo": "cfg", "applyOrder": 2 },
    { "id": "epf-load", "kind": "external-data-processor","root": "tools/ЗагрузкаЦен/src",
      "usesConfiguration": "cfg" },
    { "id": "tests",    "kind": "test-sources",           "root": "tests/yaxunit",
      "usesConfiguration": "cfg", "include": ["**/*.bsl"] }
  ],
  "exclude": ["**/logs/**", "**/temp/**"]
}`)
	return root
}

// TestLoadManifestРазбираетПримерАрхитектуры — критерий приёмки: манифест из §9
// архитектуры с пятью компонентами валидируется и разбирается полностью.
// Ожидаемые значения взяты из самого примера, а не из кода под тестом.
func TestLoadManifestРазбираетПримерАрхитектуры(t *testing.T) {
	root := проектИзАрхитектуры(t)

	m, err := workspace.LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}

	if m.Version != 1 {
		t.Errorf("version = %d, ожидалось 1", m.Version)
	}
	if m.Project != "ut-main" {
		t.Errorf("project = %q, ожидалось %q", m.Project, "ut-main")
	}
	if m.DisplayName != "УТ 11.5 (доработки)" {
		t.Errorf("displayName = %q", m.DisplayName)
	}
	if len(m.Components) != 5 {
		t.Fatalf("компонентов %d, ожидалось 5", len(m.Components))
	}
	if len(m.Exclude) != 2 || m.Exclude[0] != "**/logs/**" || m.Exclude[1] != "**/temp/**" {
		t.Errorf("exclude = %v", m.Exclude)
	}

	ожидание := map[domain.ComponentID]struct {
		kind       domain.ComponentKind
		root       string
		appliesTo  domain.ComponentID
		applyOrder int
		uses       domain.ComponentID
		include    []string
	}{
		"cfg":      {kind: domain.KindConfiguration, root: "dumps/ut_main"},
		"ext-fix":  {kind: domain.KindExtension, root: "dumps/ext_fix", appliesTo: "cfg", applyOrder: 1},
		"ext-ai":   {kind: domain.KindExtension, root: "ai-assist/src", appliesTo: "cfg", applyOrder: 2},
		"epf-load": {kind: domain.KindExternalDataProcessor, root: "tools/ЗагрузкаЦен/src", uses: "cfg"},
		"tests":    {kind: domain.KindTestSources, root: "tests/yaxunit", uses: "cfg", include: []string{"**/*.bsl"}},
	}

	for id, ожид := range ожидание {
		c, ok := m.Component(id)
		if !ok {
			t.Errorf("компонент %q не найден", id)
			continue
		}
		if c.Kind != ожид.kind {
			t.Errorf("%s: kind = %q, ожидалось %q", id, c.Kind, ожид.kind)
		}
		if c.Root != ожид.root {
			t.Errorf("%s: root = %q, ожидалось %q", id, c.Root, ожид.root)
		}
		if c.AppliesTo != ожид.appliesTo {
			t.Errorf("%s: appliesTo = %q, ожидалось %q", id, c.AppliesTo, ожид.appliesTo)
		}
		if c.ApplyOrder != ожид.applyOrder {
			t.Errorf("%s: applyOrder = %d, ожидалось %d", id, c.ApplyOrder, ожид.applyOrder)
		}
		if c.UsesConfiguration != ожид.uses {
			t.Errorf("%s: usesConfiguration = %q, ожидалось %q", id, c.UsesConfiguration, ожид.uses)
		}
		if len(c.Include) != len(ожид.include) {
			t.Errorf("%s: include = %v, ожидалось %v", id, c.Include, ожид.include)
		} else {
			for i, шаблон := range ожид.include {
				if c.Include[i] != шаблон {
					t.Errorf("%s: include[%d] = %q, ожидалось %q", id, i, c.Include[i], шаблон)
				}
			}
		}
		// AbsRoot — абсолютный путь внутри проекта, по нему индекс читает файлы.
		if !filepath.IsAbs(c.AbsRoot) {
			t.Errorf("%s: absRoot = %q, ожидался абсолютный путь", id, c.AbsRoot)
		}
		if _, err := os.Stat(filepath.Join(c.AbsRoot)); err != nil {
			t.Errorf("%s: absRoot не существует: %v", id, err)
		}
	}

	// Расширения выдаются в порядке применения — от него зависит effective view.
	exts := m.Extensions()
	if len(exts) != 2 || exts[0].ID != "ext-fix" || exts[1].ID != "ext-ai" {
		t.Errorf("Extensions() = %v, ожидался порядок ext-fix, ext-ai", exts)
	}

	cfg, ok := m.Configuration()
	if !ok || cfg.ID != "cfg" {
		t.Errorf("Configuration() = %v, %v; ожидался компонент cfg", cfg.ID, ok)
	}
}
