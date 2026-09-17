package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// --- раскладка выгрузки -------------------------------------------------
//
// Правило раскладки XML-выгрузки — одно на репозиторий (workspace.Dump*).
// Здесь обёртки под фикстуру: путь объявления объекта и путь его модуля
// раньше писались литералом (declPath("Catalog", "Товары")), а такой
// раскладки DumpConfigToFiles не производит — фикстура описывала выгрузку,
// которой нет.

func declPath(mtype, nameDisplay string) string {
	return workspace.DumpDeclarationPath(mtype, nameDisplay)
}

func commonModulePath(nameDisplay string) string {
	return workspace.DumpModulePath("CommonModule", nameDisplay, workspace.ModuleCommon)
}

// impactFixtureIDs — идентификаторы, засеянные buildImpactFixture, нужные
// тестам для построения ImpactInput.
type impactFixtureIDs struct {
	symbolUID1 string // "ПроверитьЧтоТо" (CommonModules/Utils) — цель call_edge/reference/cycle
	symbolUID4 string // "ПриСозданииНаСервере" (форма) — цель handler_binding
}

func sp() domain.Span {
	return domain.Span{StartByte: 0, EndByte: 10, StartLine: 1, StartCol: 1, EndLine: 1, EndCol: 10}
}

// buildImpactFixture строит проект с реальным store (реальный
// internal/app.Projects, реальный store.Store) и засевает его фактами
// НАПРЯМУЮ через типизированный store.WriteTx, минуя парсер и pipeline:
// find_impact тестируется через сервис (шов internal/app), а не через SQL —
// фикстура тоже не пишет SQL, только типизированные Insert*/Ensure* store'а,
// те же примитивы, которыми пользуется internal/index. Даёт точный контроль
// над всеми шестью видами рёбер сразу, без риска, что реальный
// разбор/резолвер соберёт что-то другое, чем ожидает тест.
//
// Граф (обратный BFS от каждой цели):
//
//	S1 "ПроверитьЧтоТо" <-call_edge- S2 <-call_edge- S3 <-> S2 (цикл S2/S3)
//	O1 Catalog "Товары"  <-role_right- роль "Менеджер" (Чтение)
//	O1 Catalog "Товары"  <-dependency_edge(field-typed-by)- MM1 (реквизит O3 "РеализацияТоваровУслуг")
//	O2 InformationRegister "Остатки" <-register_access(write)- S5
//	S4 "ПриСозданииНаСервере" <-handler_binding- форма F1 (владелец O1)
func buildImpactFixture(t *testing.T) (*ImpactService, impactFixtureIDs) {
	t.Helper()
	workspaceRoot := t.TempDir()
	newFixtureProject(t, workspaceRoot, "impact-fixture")
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

	ids := impactFixtureIDs{symbolUID1: "sym-s1", symbolUID4: "sym-s4"}

	err = op.Store.Write(ctx, func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		// Тот же гард, что в internal/retrieve/fixture_test.go:fileHelper:
		// путь фикстуры обязан выводиться из правила раскладки, иначе сид
		// снова опишет выгрузку, которой нет.
		file := func(rel string) (int64, error) {
			if !workspace.IsDumpConformantPath(rel) {
				return 0, fmt.Errorf("путь фикстуры %q не выводится из раскладки выгрузки — стройте его через declPath/commonModulePath/workspace.Dump*", rel)
			}
			hash, err := tx.PutBlob([]byte(rel))
			if err != nil {
				return 0, err
			}
			return tx.InsertSourceFile(store.SourceFile{
				ComponentID: "cfg", RelPath: rel, Size: int64(len(rel)), ContentHash: hash, ParserVersion: 1,
			})
		}
		module := func(rel, nameNorm, nameDisplay string, fileID int64) (int64, error) {
			id, err := tx.EnsureModule(store.Module{
				IdentityKey: "cfg\x00module\x00" + rel, ComponentID: "cfg", Kind: "CommonModule",
				NameNorm: nameNorm, NameDisplay: nameDisplay,
			})
			if err != nil {
				return 0, err
			}
			return id, tx.PutModuleCode(id, fileID)
		}
		symbol := func(uid string, moduleID, fileID int64, nameNorm, nameDisplay string) (int64, error) {
			return tx.InsertSymbol(store.Symbol{
				IdentityKey: uid, ComponentID: "cfg", UID: uid, ModuleID: moduleID, OriginFileID: fileID,
				Kind: "procedure", NameNorm: nameNorm, NameDisplay: nameDisplay, IsExport: true, Span: sp(),
			})
		}
		call := func(fileID, callerID, calleeID int64, calleeName string) error {
			refID, err := tx.InsertReference(store.Reference{
				FileID: fileID, FromSymbolID: callerID, Kind: "call", NameNorm: calleeName,
				Resolution: "resolved", TargetClass: "symbol", TargetSymbolID: calleeID,
				Confidence: 1, Layer: "base", Span: sp(),
			})
			if err != nil {
				return err
			}
			return tx.InsertCallEdge(store.CallEdge{
				CallerID: callerID, CalleeID: calleeID, CalleeNameNorm: calleeName,
				Kind: "qualified", Resolution: "resolved", Confidence: 1, RefID: refID,
			})
		}

		fUtils, err := file(commonModulePath("Utils"))
		if err != nil {
			return err
		}
		mUtils, err := module(commonModulePath("Utils"), "утилиты", "Утилиты", fUtils)
		if err != nil {
			return err
		}
		s1, err := symbol(ids.symbolUID1, mUtils, fUtils, "проверитьчтото", "ПроверитьЧтоТо")
		if err != nil {
			return err
		}

		fCaller, err := file(commonModulePath("Caller"))
		if err != nil {
			return err
		}
		mCaller, err := module(commonModulePath("Caller"), "вызывающий", "Вызывающий", fCaller)
		if err != nil {
			return err
		}
		s2, err := symbol("sym-s2", mCaller, fCaller, "вызватьпроверку", "ВызватьПроверку")
		if err != nil {
			return err
		}
		if err := call(fCaller, s2, s1, "проверитьчтото"); err != nil {
			return err
		}

		fCaller2, err := file(commonModulePath("Caller2"))
		if err != nil {
			return err
		}
		mCaller2, err := module(commonModulePath("Caller2"), "вызывающий2", "Вызывающий2", fCaller2)
		if err != nil {
			return err
		}
		s3, err := symbol("sym-s3", mCaller2, fCaller2, "вызватьвызывающий", "ВызватьВызывающий")
		if err != nil {
			return err
		}
		// цикл: S3 зовёт S2 (обычное ребро дальше по цепи от S1) И S2 зовёт S3
		// (обратное ребро) — обратный BFS от S1, дойдя до S2, а потом до S3,
		// снова упрётся в S2 через это ребро и обязан не зациклиться.
		if err := call(fCaller2, s3, s2, "вызватьпроверку"); err != nil {
			return err
		}
		if err := call(fCaller, s2, s3, "вызватьвызывающий"); err != nil {
			return err
		}

		fMeta, err := file(declPath("Catalog", "Товары"))
		if err != nil {
			return err
		}
		o1, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00Catalog\x00товары", ComponentID: "cfg", MType: "Catalog",
			NameNorm: "товары", NameDisplay: "Товары", FileID: fMeta, Layer: "base",
		})
		if err != nil {
			return err
		}

		roleID, err := tx.EnsureRole(store.Role{
			ComponentID: "cfg", NameNorm: "менеджер", NameDisplay: "Менеджер", FileID: fMeta, Layer: "base",
		})
		if err != nil {
			return err
		}
		if err := tx.InsertRoleRight(store.RoleRight{
			RoleID: roleID, ObjectID: o1, ObjectNameNorm: "catalog.товары", RightName: "Чтение",
			Value: true, SetForNewObject: false, OriginFileID: fMeta,
		}); err != nil {
			return err
		}

		fDoc, err := file(declPath("Document", "РеализацияТоваровУслуг"))
		if err != nil {
			return err
		}
		o3, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00Document\x00реализациятоваровуслуг", ComponentID: "cfg",
			MType: "Document", NameNorm: "реализациятоваровуслуг", NameDisplay: "РеализацияТоваровУслуг",
			FileID: fDoc, Layer: "base",
		})
		if err != nil {
			return err
		}
		mm1, err := tx.InsertMetadataMember(store.MetadataMember{
			IdentityKey: "cfg\x00object\x00Document\x00реализациятоваровуслуг\x00member\x00Attribute\x00\x00номенклатура",
			ComponentID: "cfg", ObjectID: o3, OriginFileID: fDoc, Kind: "Attribute",
			NameNorm: "номенклатура", NameDisplay: "Номенклатура",
		})
		if err != nil {
			return err
		}
		if err := tx.InsertDependencyEdge(store.DependencyEdge{
			Kind: "field-typed-by", FromNode: mm1, ToNode: o1, OriginFileID: fDoc, Confidence: 1, Layer: "base",
		}); err != nil {
			return err
		}

		o2, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00InformationRegister\x00остатки", ComponentID: "cfg",
			MType: "InformationRegister", NameNorm: "остатки", NameDisplay: "Остатки", FileID: fMeta, Layer: "base",
		})
		if err != nil {
			return err
		}
		// Обе стороны пары — один вызов правила: пока путь файла и путь модуля
		// строились по отдельности, вторая сторона была хвостом-литералом, а
		// совпадение держалось на том, что строки написаны рядом.
		regManagerModule := workspace.DumpModulePath("InformationRegister", "Остатки", workspace.ModuleManager)
		fReg, err := file(regManagerModule)
		if err != nil {
			return err
		}
		mReg, err := module(regManagerModule, "менеджеростатков", "МенеджерОстатков", fReg)
		if err != nil {
			return err
		}
		s5, err := symbol("sym-s5", mReg, fReg, "записатьостаток", "ЗаписатьОстаток")
		if err != nil {
			return err
		}
		if err := tx.InsertRegisterAccess(store.RegisterAccess{
			FileID: fReg, SymbolID: s5, ObjectID: o2, RegisterNameNorm: "остатки", Mode: "write",
			Static: true, Confidence: 0.95, Span: sp(),
		}); err != nil {
			return err
		}

		fForm, err := file(workspace.DumpFormModulePath("Catalog", "Товары", "ФормаЭлемента"))
		if err != nil {
			return err
		}
		mForm, err := module(workspace.DumpFormModulePath("Catalog", "Товары", "ФормаЭлемента"), "форматовары", "ФормаТовары", fForm)
		if err != nil {
			return err
		}
		s4, err := symbol(ids.symbolUID4, mForm, fForm, "присозданиинасервере", "ПриСозданииНаСервере")
		if err != nil {
			return err
		}
		formID, err := tx.EnsureForm(store.Form{
			IdentityKey: "cfg\x00form\x00Catalog.Товары.Form.ФормаЭлемента", ComponentID: "cfg",
			OwnerObjectID: o1, NameNorm: "формаэлемента", NameDisplay: "ФормаЭлемента",
		})
		if err != nil {
			return err
		}
		if err := tx.PutFormDeclaration(formID, fForm); err != nil {
			return err
		}
		return tx.InsertHandlerBinding(store.HandlerBinding{
			FormID: formID, Source: "Форма", Event: "ПриСозданииНаСервере", HandlerNameNorm: "присозданиинасервере",
			HandlerSymbolID: s4, OriginFileID: fForm, Resolution: "resolved",
		})
	})
	if err != nil {
		t.Fatalf("build fixture: %v", err)
	}

	return NewImpactService(p), ids
}

// TestImpactServiceCallChainDepthAndPath: цель — символ S1. Обратный BFS
// находит прямого вызывающего (S2, depth1, call_edge) и транзитивного (S3,
// depth2), путь до S3 называет вид ребра на каждом шаге, ранжирование ставит
// более близкий (S2) выше более дальнего (S3).
func TestImpactServiceCallChainDepthAndPath(t *testing.T) {
	svc, ids := buildImpactFixture(t)
	resp, err := svc.Impact(context.Background(), ImpactInput{
		Target: ImpactTarget{SymbolUID: ids.symbolUID1},
		Kinds:  []string{"call_edge"},
		Depth:  3, Budget: 100,
	})
	if err != nil {
		t.Fatalf("Impact: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("Items = %d, want 2 (S2 depth1, S3 depth2); items=%+v", len(resp.Items), resp.Items)
	}
	s2 := resp.Items[0]
	if s2.Depth != 1 || s2.EdgeKind != "call_edge" || !strings.Contains(s2.Display, "ВызватьПроверку") {
		t.Fatalf("Items[0] = %+v, want depth1 call_edge к ВызватьПроверку", s2)
	}
	s3 := resp.Items[1]
	if s3.Depth != 2 || !strings.Contains(s3.Display, "ВызватьВызывающий") {
		t.Fatalf("Items[1] = %+v, want depth2 -> ВызватьВызывающий", s3)
	}
	if len(s3.Path) != 2 {
		t.Fatalf("Path(S3) = %d шагов, want 2", len(s3.Path))
	}
	for i, step := range s3.Path {
		if step.EdgeKind != "call_edge" {
			t.Errorf("Path[%d].EdgeKind = %q, want call_edge", i, step.EdgeKind)
		}
	}
	if s3.Path[1].Display != s3.Display {
		t.Errorf("последний шаг пути (%q) должен совпадать с самим найденным элементом (%q)",
			s3.Path[1].Display, s3.Display)
	}
}

// TestImpactServiceCycleDoesNotHang: тот же граф, но глубина 5 — достаточно,
// чтобы обратный BFS от S1 дошёл до цикла S2<->S3 и вернулся к уже
// посещённому S2. Тест сам себе таймаут: если обход зациклится, он не
// вернётся никогда, и `go test` упадёт по таймауту пакета — здесь достаточно
// проверить, что вызов ЗАВЕРШИЛСЯ и вернул ровно двух затронутых (S2, S3),
// а не бесконечно растущий список.
func TestImpactServiceCycleDoesNotHang(t *testing.T) {
	svc, ids := buildImpactFixture(t)
	resp, err := svc.Impact(context.Background(), ImpactInput{
		Target: ImpactTarget{SymbolUID: ids.symbolUID1},
		Kinds:  []string{"call_edge"},
		Depth:  5, Budget: 100,
	})
	if err != nil {
		t.Fatalf("Impact: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("Items = %d, want 2 (цикл не должен размножать S2/S3); items=%+v", len(resp.Items), resp.Items)
	}
}

// TestImpactServiceBudgetTruncatesHonestly: budget=1 обязан остановить обход
// после первого узла и честно пометить это warning-ом impact_truncated, а не
// молча обрезать список без объяснения (критерий приёмки тикета 13).
func TestImpactServiceBudgetTruncatesHonestly(t *testing.T) {
	svc, ids := buildImpactFixture(t)
	resp, err := svc.Impact(context.Background(), ImpactInput{
		Target: ImpactTarget{SymbolUID: ids.symbolUID1},
		Kinds:  []string{"call_edge"},
		Depth:  5, Budget: 1,
	})
	if err != nil {
		t.Fatalf("Impact: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("Items = %d, want 1 (budget=1)", len(resp.Items))
	}
	found := false
	for _, w := range resp.Warnings {
		if w.Code == "impact_truncated" {
			found = true
		}
	}
	if !found {
		t.Fatalf("нет warning impact_truncated при усечении, Warnings=%+v", resp.Warnings)
	}
}

// TestImpactServiceObjectRoleRightAndDependencyEdge: цель — объект метаданных
// (Catalog.Товары). Проверяет role_right (право роли «Менеджер») и
// dependency_edge (реквизит документа, типизированный этим справочником) —
// оба вида рёбер, специфичных для объектов, а не символов.
func TestImpactServiceObjectRoleRightAndDependencyEdge(t *testing.T) {
	svc, _ := buildImpactFixture(t)
	resp, err := svc.Impact(context.Background(), ImpactInput{
		Target: ImpactTarget{ObjectType: "Catalog", ObjectName: "Товары"},
		Depth:  1, Budget: 100,
	})
	if err != nil {
		t.Fatalf("Impact: %v", err)
	}
	var sawRole, sawDep bool
	for _, it := range resp.Items {
		switch it.EdgeKind {
		case "role_right":
			sawRole = true
			if it.NodeKind != "role" || !strings.Contains(it.Display, "Менеджер") {
				t.Errorf("role_right item = %+v, want NodeKind=role, Display содержит Менеджер", it)
			}
			if !strings.Contains(it.Detail, "Чтение") {
				t.Errorf("role_right Detail = %q, want содержит Чтение", it.Detail)
			}
			if it.Confidence != 1 {
				t.Errorf("role_right Confidence = %v, want 1 (точный факт XML)", it.Confidence)
			}
		case "dependency_edge":
			sawDep = true
			if it.NodeKind != "metadata_member" || !strings.Contains(it.Display, "РеализацияТоваровУслуг") {
				t.Errorf("dependency_edge item = %+v, want NodeKind=metadata_member, Display содержит РеализацияТоваровУслуг", it)
			}
		}
	}
	if !sawRole {
		t.Error("role_right не найден в impact списке")
	}
	if !sawDep {
		t.Error("dependency_edge не найден в impact списке")
	}
}

// TestImpactServiceKindsFilterNarrowsResult: тот же объект, но kinds сужен
// до role_right — dependency_edge, реально существующий в фикстуре, обязан
// пропасть из выдачи.
func TestImpactServiceKindsFilterNarrowsResult(t *testing.T) {
	svc, _ := buildImpactFixture(t)
	resp, err := svc.Impact(context.Background(), ImpactInput{
		Target: ImpactTarget{ObjectType: "Catalog", ObjectName: "Товары"},
		Kinds:  []string{"role_right"},
		Depth:  1, Budget: 100,
	})
	if err != nil {
		t.Fatalf("Impact: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].EdgeKind != "role_right" {
		t.Fatalf("Items = %+v, want ровно один role_right", resp.Items)
	}
}

// TestImpactServiceRegisterAccessAndHandlerBinding: register_access (кто
// пишет в регистр) и handler_binding (форма, привязавшая обработчик) —
// проверяются раздельно, у каждого своя цель.
func TestImpactServiceRegisterAccessAndHandlerBinding(t *testing.T) {
	svc, ids := buildImpactFixture(t)
	ctx := context.Background()

	regResp, err := svc.Impact(ctx, ImpactInput{
		Target: ImpactTarget{ObjectType: "InformationRegister", ObjectName: "Остатки"},
		Depth:  1, Budget: 100,
	})
	if err != nil {
		t.Fatalf("Impact(register): %v", err)
	}
	if len(regResp.Items) != 1 || regResp.Items[0].EdgeKind != "register_access" {
		t.Fatalf("register Items = %+v, want ровно один register_access", regResp.Items)
	}
	if !strings.Contains(regResp.Items[0].Detail, "write") {
		t.Errorf("register_access Detail = %q, want содержит write", regResp.Items[0].Detail)
	}

	handlerResp, err := svc.Impact(ctx, ImpactInput{
		Target: ImpactTarget{SymbolUID: ids.symbolUID4},
		Depth:  1, Budget: 100,
	})
	if err != nil {
		t.Fatalf("Impact(handler): %v", err)
	}
	if len(handlerResp.Items) != 1 || handlerResp.Items[0].EdgeKind != "handler_binding" {
		t.Fatalf("handler Items = %+v, want ровно один handler_binding", handlerResp.Items)
	}
	if handlerResp.Items[0].NodeKind != "form" || !strings.Contains(handlerResp.Items[0].Display, "ФормаЭлемента") {
		t.Errorf("handler_binding item = %+v, want NodeKind=form, Display содержит ФормаЭлемента", handlerResp.Items[0])
	}
}

// TestImpactServiceTargetNotFound: символ с несуществующим uid — actionable
// not_found, а не пустой список (тот же контракт, что у остальных
// find_*-инструментов).
func TestImpactServiceTargetNotFound(t *testing.T) {
	svc, _ := buildImpactFixture(t)
	_, err := svc.Impact(context.Background(), ImpactInput{Target: ImpactTarget{SymbolUID: "does-not-exist"}})
	appErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("err = %T, want *app.Error", err)
	}
	if appErr.Code != CodeNotFound {
		t.Fatalf("code = %s, want %s", appErr.Code, CodeNotFound)
	}
}

// TestImpactServiceBadTarget: ни symbolUID, ни objectType+objectName — не
// пытается угадать, отдаёт понятную ошибку.
func TestImpactServiceBadTarget(t *testing.T) {
	svc, _ := buildImpactFixture(t)
	_, err := svc.Impact(context.Background(), ImpactInput{})
	if err == nil {
		t.Fatal("ожидалась ошибка на пустую цель")
	}
}

// Сверка с find_dependency_paths (критерий приёмки тикета 13, «на трёх
// входах») живёт НЕ здесь, а в cmd/mcp1c/idx_impact_realdump_test.go.
// Причина — internal/arch.CheckLegacyIsolation (RuleNoLegacyInNew):
// internal/app в списке «новых» пакетов, которым запрещено опираться на
// internal/source (эталон find_dependency_paths — internal/source/deppaths.go),
// и правило проверяет ВСЕ файлы пакета, включая _test.go — исключения для
// тестов в этом гарде нет и заводить его не входит в зону тикета 13. Сборка
// реального проекта над реальной выгрузкой (store.Open/index.NewService/
// workspace, без internal/source) вынесена в internal/app/impact_testsupport.go как
// NewRealDumpImpactService — единственный кусок, которому нужны внутренности
// пакета (workspaceRoot, openProject); сам вызов find_dependency_paths и
// сравнение делает cmd/mcp1c, где internal/source и так уже используется
// старыми инструментами (deppaths.go) и где на internal/app.CheckCommandLayering
// нет проблем, потому что весь доступ к store/index идёт ЧЕРЕЗ
// NewRealDumpImpactService, а не напрямую.
