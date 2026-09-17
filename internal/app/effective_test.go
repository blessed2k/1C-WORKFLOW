package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// extConfigurationXML — Configuration.xml с ConfigurationExtensionPurpose,
// нужен workspace.DetectKind, чтобы каталог распознавался как расширение
// (тот же фрагмент, что internal/workspace/manifest_test.go использует под
// именем конфигурацияXML(name, purpose)).
func extConfigurationXML(name, purpose string) string {
	return bom + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
	<Configuration uuid="0d3a94e9-6b9d-4b5a-9c1e-2f4a1d1b0002">
		<Properties>
			<Name>` + name + `</Name>
			<ConfigurationExtensionPurpose>` + purpose + `</ConfigurationExtensionPurpose>
		</Properties>
		<ChildObjects/>
	</Configuration>
</MetaDataObject>`
}

// catalogWithAttributeXML — Catalogs/Товары.xml с ровно одним реквизитом
// (минимальный набор, достаточный parse/meta — см. internal/parse/meta/object_test.go).
func catalogWithAttributeXML(attrName string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
  <Catalog uuid="22222222-2222-2222-2222-222222222222">
    <Properties>
      <Name>Товары</Name>
    </Properties>
    <ChildObjects>
      <Attribute uuid="a1">
        <Properties>
          <Name>` + attrName + `</Name>
          <Type><v8:Type>xs:string</v8:Type></Type>
        </Properties>
      </Attribute>
    </ChildObjects>
  </Catalog>
</MetaDataObject>`
}

const catalogManagerModulePath = "Catalogs/Товары/Ext/ManagerModule.bsl"

// baseManagerModuleWithTarget — модуль менеджера базового слоя: один
// экспортный метод Рассчитать — цель перехватчиков расширений.
const baseManagerModuleWithTarget = `
Функция Рассчитать() Экспорт
	Возврат 1;
КонецФункции
`

// insteadInterceptorModule строит модуль расширения, заимствовавшего
// ManagerModule и перехватывающего Рассчитать через &Вместо. prefix —
// приставка расширения в имени перехватчика ("РасшБ_", "РасшА_"): в реальном коде
// имя перехватчика НЕ совпадает с именем цели, и фикстура обязана это
// воспроизводить — иначе связь «перехватчик → цель» проходит тест и по
// старому правилу «цель = имя метода».
func insteadInterceptorModule(prefix string) string {
	return `
&Вместо("Рассчитать")
Функция ` + prefix + `Рассчитать() Экспорт
	Возврат 2;
КонецФункции
`
}

// extensionSpec — один компонент-расширение фикстуры.
type extensionSpec struct {
	id         string
	applyOrder int
	files      map[string]string
}

// newExtensionFixtureProject строит проект с базовым компонентом cfg
// (каталог Товары с реквизитом Название + ManagerModule.bsl с Рассчитать) и
// произвольным числом компонентов-расширений, применяющихся к cfg.
func newExtensionFixtureProject(t *testing.T, id domain.ProjectID, exts []extensionSpec) (*Projects, *openProject) {
	t.Helper()
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()

	baseFiles := map[string]string{
		"Configuration.xml":      конфигурацияXML("Тест"),
		"Catalogs/Товары.xml":    catalogWithAttributeXML("Название"),
		catalogManagerModulePath: baseManagerModuleWithTarget,
	}
	for rel, content := range baseFiles {
		writeFile(t, filepath.Join(projectRoot, "cfg", filepath.FromSlash(rel)), content)
	}

	components := []map[string]any{
		{"id": "cfg", "kind": "configuration", "root": "cfg"},
	}
	for _, ext := range exts {
		for rel, content := range ext.files {
			writeFile(t, filepath.Join(projectRoot, ext.id, filepath.FromSlash(rel)), content)
		}
		components = append(components, map[string]any{
			"id": ext.id, "kind": "extension", "root": ext.id,
			"appliesTo": "cfg", "applyOrder": ext.applyOrder,
		})
	}

	manifest := map[string]any{
		"version": 1, "project": string(id), "components": components,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeFile(t, filepath.Join(projectRoot, workspace.ManifestFileName), string(data))

	reg, err := workspace.OpenRegistry(workspaceRoot)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := reg.Upsert(workspace.ProjectEntry{ID: id, Root: projectRoot}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := reg.SetActiveProject(id); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	p, err := NewProjects(workspaceRoot, nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { p.Close() })

	ctx := context.Background()
	op, err := p.Active(ctx)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if _, err := op.Service.Reindex(ctx, index.ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	return p, op
}

// oneExtension — фикстура «расширение из выгрузки (или фикстуры)»: один
// компонент-расширение, заимствующий Catalogs/Товары (добавляет реквизит
// КомментарийРасширения) и ManagerModule (перехватывает Рассчитать через
// &Вместо) — критерий приёмки тикета 14, п.1: реального расширения в
// dumps/ut_demo нет (проверено: ни один Configuration.xml не несёт
// ConfigurationExtensionPurpose), фикстура — ожидаемый, явно допустимый путь.
func oneExtension() []extensionSpec {
	return []extensionSpec{{
		id: "ext", applyOrder: 1,
		files: map[string]string{
			"Configuration.xml":      extConfigurationXML("Доработки", "Customization"),
			"Catalogs/Товары.xml":    catalogWithAttributeXML("КомментарийРасширения"),
			catalogManagerModulePath: insteadInterceptorModule("РасшБ_"),
		},
	}}
}

// TestExtensionIndexedAsSeparateLayer — критерий приёмки п.1: расширение из
// фикстуры индексируется как отдельный слой (свой component, свой symbol).
func TestExtensionIndexedAsSeparateLayer(t *testing.T) {
	p, _ := newExtensionFixtureProject(t, "ext-layer", oneExtension())
	symSvc := NewSymbolService(p)
	ctx := context.Background()

	// Имена методов слоёв разные (Рассчитать и РасшБ_Рассчитать) — подстрока
	// "Рассчитать" ловит оба, и именно это доказывает, что расширение
	// проиндексировано отдельным слоем со СВОИМ символом.
	resp, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "Рассчитать"})
	if err != nil {
		t.Fatalf("FindSymbol: %v", err)
	}
	// Число утверждается рядом с именами: карта component→name молча
	// перезаписала бы лишний дубль символа в слое, и слой с двумя
	// Рассчитать выглядел бы как слой с одним.
	if len(resp.Items) != 2 {
		t.Fatalf("Items = %d, want 2 (по одному символу на слой: base + ext): %+v", len(resp.Items), resp.Items)
	}
	byComponent := map[domain.ComponentID]string{}
	for _, it := range resp.Items {
		byComponent[it.Component] = it.Name
	}
	if byComponent["cfg"] != "Рассчитать" {
		t.Fatalf("символ базового слоя = %q, want Рассчитать: %+v", byComponent["cfg"], resp.Items)
	}
	if byComponent["ext"] != "РасшБ_Рассчитать" {
		t.Fatalf("символ расширения = %q, want РасшБ_Рассчитать: %+v", byComponent["ext"], resp.Items)
	}
}

// twoConflictingExtensions — два расширения, каждое перехватывает
// Рассчитать через &Вместо — фикстура критерия приёмки п.4 (конфликт).
// Имена перехватчиков РАЗНЫЕ (РасшА_/РасшБ_, как в реальных расширениях): конфликт
// обязан считаться по цели, а не по имени перехватчика.
func twoConflictingExtensions() []extensionSpec {
	return []extensionSpec{
		{
			id: "exta", applyOrder: 1,
			files: map[string]string{
				"Configuration.xml":      extConfigurationXML("ДоработкаA", "CustomizationA"),
				catalogManagerModulePath: insteadInterceptorModule("РасшА_"),
			},
		},
		{
			id: "extb", applyOrder: 2,
			files: map[string]string{
				"Configuration.xml":      extConfigurationXML("ДоработкаB", "CustomizationB"),
				catalogManagerModulePath: insteadInterceptorModule("РасшБ_"),
			},
		},
	}
}

// mustGetSymbol находит базовый символ Рассчитать (cfg) и вызывает GetSymbol
// с переданным view — общий шаг тестов ниже.
func mustGetSymbol(t *testing.T, p *Projects, view string) Response[SymbolDetail] {
	t.Helper()
	ctx := context.Background()
	symSvc := NewSymbolService(p)
	found, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "Рассчитать", Component: "cfg"})
	if err != nil {
		t.Fatalf("FindSymbol(cfg): %v", err)
	}
	if len(found.Items) != 1 {
		t.Fatalf("FindSymbol(cfg).Items = %+v, want ровно один (базовый слой)", found.Items)
	}
	resp, err := symSvc.GetSymbol(ctx, GetSymbolInput{UID: found.Items[0].UID, View: view})
	if err != nil {
		t.Fatalf("GetSymbol(view=%s): %v", view, err)
	}
	return resp
}

// TestGetSymbolEffectiveLinksInterceptorToBaseSymbol — критерий приёмки п.2
// и п.3: view=effective на базовом символе, заимствованном расширением,
// отдаёт связанный перехватчик — вид, слой (provenance), имя.
func TestGetSymbolEffectiveLinksInterceptorToBaseSymbol(t *testing.T) {
	p, _ := newExtensionFixtureProject(t, "sym-effective", oneExtension())
	resp := mustGetSymbol(t, p, "effective")
	item := resp.Items[0]
	if len(item.Intercepts) != 1 {
		t.Fatalf("Intercepts = %+v, want ровно один перехватчик из ext", item.Intercepts)
	}
	ic := item.Intercepts[0]
	if ic.Kind != "Вместо" {
		t.Errorf("Kind = %q, want Вместо", ic.Kind)
	}
	if ic.Layer != "ext" {
		t.Errorf("Layer = %q, want ext (provenance перехватчика)", ic.Layer)
	}
	if ic.InterceptorName != "расшб_рассчитать" {
		t.Errorf("InterceptorName = %q, want расшб_рассчитать (имя МЕТОДА расширения)", ic.InterceptorName)
	}
	if ic.TargetName != "рассчитать" {
		t.Errorf("TargetName = %q, want рассчитать (аргумент аннотации)", ic.TargetName)
	}
	if ic.Confidence != 1 {
		t.Errorf("Confidence = %v, want 1 (факт точный)", ic.Confidence)
	}
}

// TestGetSymbolRawHasNoIntercepts — регрессия: view=raw (и умолчание) не
// считает перехватчики вовсе, даже когда расширение есть.
func TestGetSymbolRawHasNoIntercepts(t *testing.T) {
	p, _ := newExtensionFixtureProject(t, "sym-raw", oneExtension())
	for _, view := range []string{"", "raw"} {
		resp := mustGetSymbol(t, p, view)
		if len(resp.Items[0].Intercepts) != 0 {
			t.Fatalf("view=%q: Intercepts = %+v, want пусто", view, resp.Items[0].Intercepts)
		}
		if len(resp.Warnings) != 0 {
			t.Fatalf("view=%q: Warnings = %+v, want пусто", view, resp.Warnings)
		}
	}
}

// TestGetSymbolEffectiveTwoInsteadConflict — критерий приёмки п.4: два
// расширения перехватывают один метод через &Вместо -> diagnostic (Warning)
// с обоими слоями, не молчаливый выбор.
func TestGetSymbolEffectiveTwoInsteadConflict(t *testing.T) {
	p, _ := newExtensionFixtureProject(t, "sym-conflict", twoConflictingExtensions())
	resp := mustGetSymbol(t, p, "effective")
	if len(resp.Items[0].Intercepts) != 2 {
		t.Fatalf("Intercepts = %+v, want оба перехватчика (exta и extb)", resp.Items[0].Intercepts)
	}
	var conflict *Warning
	for i := range resp.Warnings {
		if resp.Warnings[i].Code == "instead_conflict" {
			conflict = &resp.Warnings[i]
		}
	}
	if conflict == nil {
		t.Fatalf("Warnings = %+v, want предупреждение instead_conflict", resp.Warnings)
	}
	if !strings.Contains(conflict.Message, "exta") || !strings.Contains(conflict.Message, "extb") {
		t.Errorf("Message = %q, want оба слоя exta и extb", conflict.Message)
	}
}

// TestGetSymbolInvalidViewIsError — опечатка в view — ошибка, не молчаливый
// откат на raw (тикет 14 меняет поведение тасков 12/13: теперь effective
// реализован, и "effectiv" не должен тихо стать raw).
func TestGetSymbolInvalidViewIsError(t *testing.T) {
	p, _ := newExtensionFixtureProject(t, "sym-badview", nil)
	_, err := mustGetSymbolErr(t, p, "effectiv")
	appErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("err = %T, want *app.Error", err)
	}
	if appErr.Code != CodeNotFound {
		t.Fatalf("code = %s, want %s", appErr.Code, CodeNotFound)
	}
}

func mustGetSymbolErr(t *testing.T, p *Projects, view string) (Response[SymbolDetail], error) {
	t.Helper()
	ctx := context.Background()
	symSvc := NewSymbolService(p)
	found, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "Рассчитать", Component: "cfg"})
	if err != nil {
		t.Fatalf("FindSymbol(cfg): %v", err)
	}
	return symSvc.GetSymbol(ctx, GetSymbolInput{UID: found.Items[0].UID, View: view})
}

// TestGetModuleStructureEffectiveAttachesInterceptsPerSymbol — get_module_structure
// с view=effective помечает каждый символ модуля его собственными
// перехватчиками (не общим списком на весь модуль).
func TestGetModuleStructureEffectiveAttachesInterceptsPerSymbol(t *testing.T) {
	p, _ := newExtensionFixtureProject(t, "modstruct-effective", oneExtension())
	symSvc := NewSymbolService(p)
	ctx := context.Background()

	resp, err := symSvc.GetModuleStructure(ctx, GetModuleStructureInput{
		Module: catalogManagerModulePath, Component: "cfg", View: "effective",
	})
	if err != nil {
		t.Fatalf("GetModuleStructure: %v", err)
	}
	if len(resp.Items) != 1 || len(resp.Items[0].Symbols) != 1 {
		t.Fatalf("Items = %+v, want ровно один символ (Рассчитать)", resp.Items)
	}
	sym := resp.Items[0].Symbols[0]
	if len(sym.Intercepts) != 1 || sym.Intercepts[0].Layer != "ext" {
		t.Fatalf("Symbols[0].Intercepts = %+v, want один перехватчик из ext", sym.Intercepts)
	}
}

// TestGetObjectEffectiveMergesMembersAcrossLayers — критерий приёмки п.3:
// view=effective на заимствованном объекте отдаёт слитую картину (реквизиты
// базового слоя + добавленные расширением), каждый факт называет свой слой.
func TestGetObjectEffectiveMergesMembersAcrossLayers(t *testing.T) {
	p, _ := newExtensionFixtureProject(t, "obj-effective", oneExtension())
	metaSvc := NewMetadataService(p)
	ctx := context.Background()

	raw, err := metaSvc.GetObject(ctx, GetObjectInput{Type: "Catalog", Name: "Товары"})
	if err != nil {
		t.Fatalf("GetObject(raw): %v", err)
	}
	if len(raw.Items) != 1 || len(raw.Items[0].Members) != 1 || raw.Items[0].Members[0].Name != "Название" {
		t.Fatalf("GetObject(raw).Members = %+v, want ровно [Название] (регрессия: raw не сливает слои)", raw.Items[0].Members)
	}

	eff, err := metaSvc.GetObject(ctx, GetObjectInput{Type: "Catalog", Name: "Товары", View: "effective"})
	if err != nil {
		t.Fatalf("GetObject(effective): %v", err)
	}
	if len(eff.Items) != 1 || len(eff.Items[0].Members) != 2 {
		t.Fatalf("GetObject(effective).Members = %+v, want 2 (базовый + добавленный расширением)", eff.Items[0].Members)
	}
	byName := map[string]string{}
	for _, m := range eff.Items[0].Members {
		byName[m.Name] = m.Layer
	}
	if byName["Название"] != "base" {
		t.Errorf("Название.Layer = %q, want base", byName["Название"])
	}
	if byName["КомментарийРасширения"] != "ext" {
		t.Errorf("КомментарийРасширения.Layer = %q, want ext", byName["КомментарийРасширения"])
	}
}

// TestFindReferencesEffectiveWarnsAboutIntercept — find_references(view=
// effective) на перехваченном символе называет перехватчика через Warning
// (см. doc-комментарий FindReferencesInput.View: не в каждой строке
// выдачи — только по корневому символу запроса).
func TestFindReferencesEffectiveWarnsAboutIntercept(t *testing.T) {
	p, _ := newExtensionFixtureProject(t, "refs-effective", oneExtension())
	ctx := context.Background()
	symSvc, graphSvc := NewSymbolService(p), NewGraphService(p)

	found, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "Рассчитать", Component: "cfg"})
	if err != nil || len(found.Items) != 1 {
		t.Fatalf("FindSymbol(cfg): %+v %v", found.Items, err)
	}
	uid := found.Items[0].UID

	raw, err := graphSvc.FindReferences(ctx, FindReferencesInput{UID: uid})
	if err != nil {
		t.Fatalf("FindReferences(raw): %v", err)
	}
	if len(raw.Warnings) != 0 {
		t.Fatalf("FindReferences(raw).Warnings = %+v, want пусто (регрессия)", raw.Warnings)
	}

	eff, err := graphSvc.FindReferences(ctx, FindReferencesInput{UID: uid, View: "effective"})
	if err != nil {
		t.Fatalf("FindReferences(effective): %v", err)
	}
	found2 := false
	for _, w := range eff.Warnings {
		if w.Code == "intercepted_by_extension" {
			found2 = true
		}
	}
	if !found2 {
		t.Fatalf("FindReferences(effective).Warnings = %+v, want intercepted_by_extension", eff.Warnings)
	}
}

// TestFindImpactEffectiveAddsInterceptItem — find_impact(view=effective) на
// перехваченном базовом символе включает перехватчик как ImpactItem
// глубины 1, edgeKind=intercepts, со своим слоем; view=raw не видит его.
func TestFindImpactEffectiveAddsInterceptItem(t *testing.T) {
	p, _ := newExtensionFixtureProject(t, "impact-effective", oneExtension())
	ctx := context.Background()
	symSvc, impactSvc := NewSymbolService(p), NewImpactService(p)

	found, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "Рассчитать", Component: "cfg"})
	if err != nil || len(found.Items) != 1 {
		t.Fatalf("FindSymbol(cfg): %+v %v", found.Items, err)
	}
	uid := found.Items[0].UID

	raw, err := impactSvc.Impact(ctx, ImpactInput{Target: ImpactTarget{SymbolUID: uid}})
	if err != nil {
		t.Fatalf("Impact(raw): %v", err)
	}
	for _, it := range raw.Items {
		if it.EdgeKind == "intercepts" {
			t.Fatalf("Impact(raw) содержит intercepts-элемент, не должен: %+v", it)
		}
	}

	eff, err := impactSvc.Impact(ctx, ImpactInput{Target: ImpactTarget{SymbolUID: uid}, View: "effective"})
	if err != nil {
		t.Fatalf("Impact(effective): %v", err)
	}
	var found3 bool
	for _, it := range eff.Items {
		if it.EdgeKind == "intercepts" && it.Layer == "ext" && it.Depth == 1 {
			found3 = true
		}
	}
	if !found3 {
		t.Fatalf("Impact(effective).Items = %+v, want элемент intercepts из ext на глубине 1", eff.Items)
	}
}

// TestFindImpactInvalidViewIsError — опечатка в view — ошибка (не
// молчаливый откат на raw, как было до тикета 14).
func TestFindImpactInvalidViewIsError(t *testing.T) {
	p, _ := newExtensionFixtureProject(t, "impact-badview", nil)
	ctx := context.Background()
	symSvc, impactSvc := NewSymbolService(p), NewImpactService(p)

	found, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "Рассчитать", Component: "cfg"})
	if err != nil || len(found.Items) != 1 {
		t.Fatalf("FindSymbol(cfg): %+v %v", found.Items, err)
	}
	_, err = impactSvc.Impact(ctx, ImpactInput{Target: ImpactTarget{SymbolUID: found.Items[0].UID}, View: "effectiv"})
	appErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("err = %T, want *app.Error", err)
	}
	if appErr.Code != CodeNotFound {
		t.Fatalf("code = %s, want %s", appErr.Code, CodeNotFound)
	}
}
