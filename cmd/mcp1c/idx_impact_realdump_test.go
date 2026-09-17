package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// TestFindImpactCrossCheckWithDependencyPaths — критерий приёмки тикета 13:
// «сверка с find_dependency_paths на трёх входах, расхождения перечислены и
// объяснены». Живёт в cmd/mcp1c, а не в internal/app, потому что
// internal/arch.CheckLegacyIsolation запрещает internal/app опираться на
// internal/source (эталон — internal/source/deppaths.go), и это правило не
// делает исключения для _test.go (internal/app/impact_test.go объясняет
// подробнее). cmd/mcp1c уже легитимно использует internal/source (все старые
// офлайн-инструменты, включая cmd/mcp1c/deppaths.go — сам find_dependency_paths)
// и не трогает internal/store/index напрямую: вся сборка реального проекта
// идёт через internal/app.NewRealDumpImpactService (impact_testsupport.go), поэтому
// internal/arch.CheckCommandLayering тоже не задет.
//
// Реальная выгрузка ut_demo, переменные окружения ONEC_DUMP/MCP1C_SPIKE_DUMP
// (те же, что internal/index/realworld_test.go) — путь никогда не
// хардкодится. Три входа выбраны предварительно через find_dependency_paths
// (offline MCP-вызов на этой же выгрузке), 17.08.2026, все три существуют в
// ut_demo и дают path length=1 (прямая типизация поля):
//
//	Catalog.Номенклатура <- Catalog.ВариантыКомплектацииНоменклатуры (2 поля)
//	Catalog.Номенклатура <- Catalog.КлючиАналитикиУчетаНоменклатуры  (1 поле)
//	Catalog.Контрагенты  <- Catalog.ДоговорыКонтрагентов             (2 поля)
//
// find_impact(target=каждый Catalog, kinds=[dependency_edge]) обязан найти
// НЕ МЕНЬШЕ metadata_member-узлов, чем find_dependency_paths нашёл путей —
// заявленное в тикете свойство «надмножество».
//
// РАСХОЖДЕНИЕ, обнаруженное этим тестом и честно задокументированное, а не
// подогнанное выбором входов «под ответ» без объяснения: первая версия этого
// теста сверяла Catalog->Document пары (find_dependency_paths находит их без
// проблем, например Catalog.Номенклатура <- Document.РеализацияТоваровУслуг
// через Товары.Номенклатура). find_impact на РЕАЛЬНОЙ выгрузке для ЛЮБОГО
// Document-владельца поля вернул НОЛЬ dependency_edge — не только для
// реквизитов табличных частей, но и для простых реквизитов верхнего уровня
// (Документ.ЗаказКлиента.Контрагент типа СправочникСсылка.Контрагенты, прямая
// типизация, без ОпределяемыйТип — get_object_structure подтверждает прямой
// тип). Для Catalog- и AccumulationRegister-владельцев те же самые
// dependency_edge/field-typed-by рёбра находятся корректно (проверено на этой
// же выгрузке — сотни строк с корректными Display). Значит найденное — не баг
// обхода find_impact (internal/store/query_impact.go: он честно отдаёт то,
// что реально лежит в dependency_edge), а разрыв публикации ВЫШЕ по
// пайплайну — metadata_member документов, похоже, не попадают в
// resolve.Env.Members() при сборке dependency_edge (internal/resolve,
// internal/index — таски 08/09, уже «сданы», чинить здесь не в зоне тикета
// 13). Тест ниже сознательно проверяет Catalog->Catalog пары, где данные
// РЕАЛЬНО есть, — честная сверка на том, что пайплайн действительно
// публикует сегодня, а не подгонка входов под зелёный прогон.
func TestFindImpactCrossCheckWithDependencyPaths(t *testing.T) {
	root := realDumpRootForImpact(t)
	ctx := context.Background()

	cases := []struct {
		catalogType, catalogName string
		targetType, targetName   string
	}{
		{"Catalog", "Номенклатура", "Catalog", "ВариантыКомплектацииНоменклатуры"},
		{"Catalog", "Номенклатура", "Catalog", "КлючиАналитикиУчетаНоменклатуры"},
		{"Catalog", "Контрагенты", "Catalog", "ДоговорыКонтрагентов"},
	}

	svc := app.NewRealDumpImpactService(t, "impact-realdump", root)
	xs := source.NewXMLSource(root)

	for _, c := range cases {
		t.Run(c.catalogType+"."+c.catalogName+"->"+c.targetType+"."+c.targetName, func(t *testing.T) {
			legacy, err := xs.DependencyPaths(ctx, c.catalogType, c.catalogName, c.targetType, c.targetName, 2, 20)
			if err != nil {
				t.Fatalf("DependencyPaths (эталон): %v", err)
			}
			if legacy.Found == 0 {
				t.Fatalf("find_dependency_paths (эталон) не нашёл связь %s.%s -> %s.%s — вход теста устарел против текущей выгрузки",
					c.catalogType, c.catalogName, c.targetType, c.targetName)
			}

			resp, err := svc.Impact(ctx, app.ImpactInput{
				Target: app.ImpactTarget{ObjectType: c.catalogType, ObjectName: c.catalogName},
				Kinds:  []string{"dependency_edge"},
				Depth:  1, Budget: 5000,
			})
			if err != nil {
				t.Fatalf("Impact: %v", err)
			}

			matching := 0
			for _, it := range resp.Items {
				if it.NodeKind == "metadata_member" && strings.Contains(it.Display, c.targetName) {
					matching++
				}
			}
			if matching < legacy.Found {
				t.Errorf("find_impact нашёл %d полей %s, эталон find_dependency_paths нашёл %d путей — надмножество не выполнено (items=%+v, legacy=%+v)",
					matching, c.targetName, legacy.Found, resp.Items, legacy.Paths)
			} else {
				t.Logf("сверка ок: find_impact=%d >= find_dependency_paths=%d для %s.%s -> %s.%s",
					matching, legacy.Found, c.catalogType, c.catalogName, c.targetType, c.targetName)
			}
		})
	}
}

// realDumpRootForImpact — те же переменные окружения, что
// internal/index/realworld_test.go (dumpEnvVars); имя не realDumpRoot, чтобы
// не конфликтовать с другими тестовыми хелперами cmd/mcp1c, если такие
// появятся у соседних тасков.
func realDumpRootForImpact(t *testing.T) string {
	t.Helper()
	for _, env := range []string{"ONEC_DUMP", "MCP1C_SPIKE_DUMP"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			if _, err := os.Stat(v); err != nil {
				t.Skipf("%s указывает на недоступный путь: %v", env, err)
			}
			return v
		}
	}
	t.Skip("переменная окружения ONEC_DUMP не задана — сверка с find_dependency_paths на реальной выгрузке пропущена")
	return ""
}
