package retrieve

import (
	"context"
	"fmt"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// Фикстуры этого пакета строятся ТЕМ ЖЕ приёмом, что
// internal/app/impact_test.go:buildImpactFixture — типизированные Insert*/
// Ensure* store.WriteTx НАПРЯМУЮ, минуя парсер/pipeline: Build читает только
// tx, и это даёт точный контроль над каждым фактом без риска, что
// разбор/резолвер соберёт что-то другое, чем ожидает тест. Ожидаемые
// значения тестов вычислены ВРУЧНУЮ по тому, что здесь заведено (не по тому,
// что вернул сам Build).

// --- раскладка выгрузки -------------------------------------------------
//
// Правило раскладки XML-выгрузки живёт в ОДНОМ месте — workspace.Dump*.
// Здесь только короткие обёртки под фикстуры: пока путь объявления и путь
// модуля пишутся литералом в каждом сиде, они снова разъезжаются с реальной
// выгрузкой: регрессия «обработчик чужого документа» родилась ровно так —
// фикстура описывала "Documents/Заказ/Заказ.xml", которого
// DumpConfigToFiles не производит, и зелёный прогон ничего не значил.

func declPath(mtype, nameDisplay string) string {
	return workspace.DumpDeclarationPath(mtype, nameDisplay)
}

func objModulePath(mtype, nameDisplay string) string {
	return workspace.DumpModulePath(mtype, nameDisplay, workspace.ModuleObject)
}

func commonModulePath(nameDisplay string) string {
	return workspace.DumpModulePath("CommonModule", nameDisplay, workspace.ModuleCommon)
}

func formModulePathOf(mtype, nameDisplay, formName string) string {
	return workspace.DumpFormModulePath(mtype, nameDisplay, formName)
}

func sp() domain.Span {
	return domain.Span{StartByte: 0, EndByte: 1, StartLine: 1, StartCol: 1, EndLine: 1, EndCol: 1}
}

func spanOf(start, end int) domain.Span {
	return domain.Span{StartByte: start, EndByte: end, StartLine: 1, StartCol: 1, EndLine: 1, EndCol: 1}
}

// openFixtureStore открывает пустой store в temp-каталоге.
func openFixtureStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir, store.Options{ProjectID: domain.ProjectID("retrieve-fixture"), StateDirName: workspace.RegistryDirName})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// fixtureIDs — идентификаторы, засеянные seedScenarioFixture, нужные тестам
// напрямую (uid'ы, куда указывают anchors).
type fixtureIDs struct {
	zapolnitStatusUID string
	poluchitCenuUID   string
	skladPriIzmUID    string
}

// fileHelper — регистрирует файл с текстовым содержимым (для символов с
// проверяемым телом) и возвращает fileID.
//
// Путь ПРОВЕРЯЕТСЯ на выводимость из правила раскладки. Без этой проверки
// помощники выше ничего не обязывают: следующий сид напишет путь литералом
// мимо них, скомпилируется и останется зелёным — ровно так фикстуры и
// разъехались с реальной выгрузкой в прошлый раз. Проверка формы, а не
// конкретного объекта: намеренно неконформная пара «файл объявления назван
// не по объекту» (postingfallback_test.go) остаётся выразимой.
func fileHelper(t *testing.T, tx *store.WriteTx, componentID, rel, content string) int64 {
	t.Helper()
	if !workspace.IsDumpConformantPath(rel) {
		t.Fatalf("путь фикстуры %q не выводится из раскладки выгрузки — стройте его через declPath/objModulePath/commonModulePath/formModulePathOf", rel)
	}
	hash, err := tx.PutBlob([]byte(content))
	if err != nil {
		t.Fatalf("PutBlob(%s): %v", rel, err)
	}
	id, err := tx.InsertSourceFile(store.SourceFile{
		ComponentID: componentID, RelPath: rel, Size: int64(len(content)), ContentHash: hash, ParserVersion: 1,
	})
	if err != nil {
		t.Fatalf("InsertSourceFile(%s): %v", rel, err)
	}
	return id
}

func moduleHelper(t *testing.T, tx *store.WriteTx, componentID, rel, nameNorm, nameDisplay string, fileID int64) int64 {
	t.Helper()
	id, err := tx.EnsureModule(store.Module{
		IdentityKey: componentID + "\x00module\x00" + rel, ComponentID: componentID, Kind: "CommonModule",
		NameNorm: nameNorm, NameDisplay: nameDisplay,
	})
	if err != nil {
		t.Fatalf("EnsureModule(%s): %v", rel, err)
	}
	if err := tx.PutModuleCode(id, fileID); err != nil {
		t.Fatalf("PutModuleCode(%s): %v", rel, err)
	}
	return id
}

type symbolSpec struct {
	uid, componentID, nameNorm, nameDisplay, kind string
	moduleID, fileID                              int64
	export                                        bool
	span                                          domain.Span
}

func symbolHelper(t *testing.T, tx *store.WriteTx, s symbolSpec) int64 {
	t.Helper()
	id, err := tx.InsertSymbol(store.Symbol{
		IdentityKey: s.uid, ComponentID: s.componentID, UID: s.uid, ModuleID: s.moduleID, OriginFileID: s.fileID,
		Kind: s.kind, NameNorm: s.nameNorm, NameDisplay: s.nameDisplay, IsExport: s.export, Span: s.span,
	})
	if err != nil {
		t.Fatalf("InsertSymbol(%s): %v", s.nameDisplay, err)
	}
	return id
}

func callHelper(t *testing.T, tx *store.WriteTx, fileID, callerID, calleeID int64, calleeName string) {
	t.Helper()
	refID, err := tx.InsertReference(store.Reference{
		FileID: fileID, FromSymbolID: callerID, Kind: "call", NameNorm: calleeName,
		Resolution: "resolved", TargetClass: "symbol", TargetSymbolID: calleeID,
		Confidence: 1, Layer: "base", Span: sp(),
	})
	if err != nil {
		t.Fatalf("InsertReference: %v", err)
	}
	if err := tx.InsertCallEdge(store.CallEdge{
		CallerID: callerID, CalleeID: calleeID, CalleeNameNorm: calleeName,
		Kind: "qualified", Resolution: "resolved", Confidence: 1, RefID: refID,
	}); err != nil {
		t.Fatalf("InsertCallEdge: %v", err)
	}
}

// seedScenarioFixture строит один проект (компоненты "cfg" и "ext"),
// покрывающий все пять сценариев §25 сразу — общий, чтобы не открывать
// пять раз store в каждом тесте.
//
// Данные (перечислены явно — ожидаемые значения тестов читаются отсюда, не
// из вывода Build):
//
//   - CommonModules/РаботаСЗаказами.ЗаполнитьСтатус (export, короткое тело
//     ~90 символов) вызывает ПроверитьЗаказ (callee); её саму вызывает
//     ОбработатьЗаказы (caller). Внутри тела — запрос по таблице "Заказы"
//     (query + query_reference kind=table).
//   - CommonModules/ОбщегоНазначения27.ПолучитьЦену (export) — 10
//     вызывающих символов в 10 разных модулях (references+callers), плюс
//     символ с тем же именем в компоненте "ext" (интерцептор-кандидат).
//   - InformationRegister ТоварыНаСкладах: СписатьТовар (write),
//     ОприходоватьТовар (movement), ПолучитьОстаток (read, НЕ обязателен).
//   - Document Заказ: форма ФормаДокумента, элемент "Склад" (DataPath
//     "Объект.Склад"), обработчик СкладПриИзменении (ПриИзменении), внутри
//     которого вызов ОбновитьЦеныНаСервере; реквизит "Склад".
//   - Document ЗаказКлиента: 2 реквизита, форма, роль "Менеджер" (право
//     "Чтение"), запрос, использующий объект (query_reference).
func seedScenarioFixture(t *testing.T, st *store.Store) fixtureIDs {
	t.Helper()
	ctx := context.Background()
	var ids fixtureIDs

	err := st.Write(ctx, func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		if err := tx.UpsertComponent(store.Component{ID: "ext", Kind: "extension", Root: "ext", ApplyOrder: 1}); err != nil {
			return err
		}

		// --- bugfix: РаботаСЗаказами.ЗаполнитьСтатус ---
		fWork := fileHelper(t, tx, "cfg", commonModulePath("РаботаСЗаказами"),
			"Процедура ЗаполнитьСтатус(Заказ) Экспорт\n\tПроверитьЗаказ(Заказ);\nКонецПроцедуры")
		mWork := moduleHelper(t, tx, "cfg", commonModulePath("РаботаСЗаказами"), "работасзаказами", "РаботаСЗаказами", fWork)
		ids.zapolnitStatusUID = "sym-zapolnit-status"
		sZapolnit := symbolHelper(t, tx, symbolSpec{
			uid: ids.zapolnitStatusUID, componentID: "cfg", nameNorm: "заполнитьстатус", nameDisplay: "ЗаполнитьСтатус",
			kind: "procedure", moduleID: mWork, fileID: fWork, export: true, span: spanOf(0, 68),
		})
		sProverit := symbolHelper(t, tx, symbolSpec{
			uid: "sym-proverit-zakaz", componentID: "cfg", nameNorm: "проверитьзаказ", nameDisplay: "ПроверитьЗаказ",
			kind: "procedure", moduleID: mWork, fileID: fWork, export: false, span: sp(),
		})
		callHelper(t, tx, fWork, sZapolnit, sProverit, "проверитьзаказ")

		fObr := fileHelper(t, tx, "cfg", commonModulePath("Обработчики"), "// caller module")
		mObr := moduleHelper(t, tx, "cfg", commonModulePath("Обработчики"), "обработчики", "Обработчики", fObr)
		sObr := symbolHelper(t, tx, symbolSpec{
			uid: "sym-obrabotat-zakazy", componentID: "cfg", nameNorm: "обработатьзаказы", nameDisplay: "ОбработатьЗаказы",
			kind: "procedure", moduleID: mObr, fileID: fObr, export: true, span: sp(),
		})
		callHelper(t, tx, fObr, sObr, sZapolnit, "заполнитьстатус")

		qID, err := tx.InsertQuery(store.Query{
			IdentityKey: "cfg\x00query\x001", ComponentID: "cfg", SymbolID: sZapolnit, FileID: fWork,
			Span: spanOf(10, 40), Staticity: "static", Text: "ВЫБРАТЬ Заказы.Ссылка ИЗ Документ.Заказы КАК Заказы",
			Confidence: 1,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertQueryReference(store.QueryReference{
			QueryID: qID, Kind: "table", NameNorm: "заказы", SpanStart: 20, SpanEnd: 26,
		}); err != nil {
			return err
		}

		// --- signature-change: ОбщегоНазначения27.ПолучитьЦену, 10 callers ---
		fOZ := fileHelper(t, tx, "cfg", commonModulePath("ОбщегоНазначения27"),
			"Функция ПолучитьЦену(Номенклатура) Экспорт\n\tВозврат 0;\nКонецФункции")
		mOZ := moduleHelper(t, tx, "cfg", commonModulePath("ОбщегоНазначения27"), "общегоназначения27", "ОбщегоНазначения27", fOZ)
		ids.poluchitCenuUID = "sym-poluchit-cenu"
		sPolCenu := symbolHelper(t, tx, symbolSpec{
			uid: ids.poluchitCenuUID, componentID: "cfg", nameNorm: "получитьцену", nameDisplay: "ПолучитьЦену",
			kind: "function", moduleID: mOZ, fileID: fOZ, export: true, span: spanOf(0, 40),
		})
		for i := 0; i < 10; i++ {
			rel := commonModulePath(fmt.Sprintf("ВызывающийМодульНомерНомерНомерНомер%02d", i))
			fc := fileHelper(t, tx, "cfg", rel, "// caller "+fmt.Sprint(i))
			mc := moduleHelper(t, tx, "cfg", rel, fmt.Sprintf("вызывающиймодульномерномерномерномер%02d", i), fmt.Sprintf("ВызывающийМодульНомерНомерНомерНомер%02d", i), fc)
			sc := symbolHelper(t, tx, symbolSpec{
				uid: fmt.Sprintf("sym-caller-%02d", i), componentID: "cfg",
				nameNorm: fmt.Sprintf("вызватьполучитьцену%02d", i), nameDisplay: fmt.Sprintf("ВызватьПолучитьЦену%02d", i),
				kind: "procedure", moduleID: mc, fileID: fc, export: true, span: sp(),
			})
			callHelper(t, tx, fc, sc, sPolCenu, "получитьцену")
		}
		fExtOZ := fileHelper(t, tx, "ext", commonModulePath("ОбщегоНазначения27"), "// перехватчик")
		mExtOZ := moduleHelper(t, tx, "ext", commonModulePath("ОбщегоНазначения27"), "общегоназначения27", "ОбщегоНазначения27", fExtOZ)
		symbolHelper(t, tx, symbolSpec{
			uid: "sym-poluchit-cenu-ext", componentID: "ext", nameNorm: "получитьцену", nameDisplay: "ПолучитьЦену",
			kind: "function", moduleID: mExtOZ, fileID: fExtOZ, export: true, span: sp(),
		})

		// --- register: InformationRegister ТоварыНаСкладах ---
		fRegMeta := fileHelper(t, tx, "cfg", declPath("InformationRegister", "ТоварыНаСкладах"), "<meta/>")
		oReg, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00InformationRegister\x00товарынаскладах", ComponentID: "cfg",
			MType: "InformationRegister", NameNorm: "товарынаскладах", NameDisplay: "ТоварыНаСкладах",
			FileID: fRegMeta, Layer: "base",
		})
		if err != nil {
			return err
		}
		fSpisat := fileHelper(t, tx, "cfg", objModulePath("Document", "РасходнаяНакладная"), "// списание")
		mSpisat := moduleHelper(t, tx, "cfg", objModulePath("Document", "РасходнаяНакладная"), "расходнаянакладная", "РасходнаяНакладная", fSpisat)
		sSpisat := symbolHelper(t, tx, symbolSpec{
			uid: "sym-spisat-tovar", componentID: "cfg", nameNorm: "списатьтовар", nameDisplay: "СписатьТовар",
			kind: "procedure", moduleID: mSpisat, fileID: fSpisat, export: false, span: sp(),
		})
		if err := tx.InsertRegisterAccess(store.RegisterAccess{
			FileID: fSpisat, SymbolID: sSpisat, ObjectID: oReg, RegisterNameNorm: "товарынаскладах", Mode: "write",
			Static: true, Confidence: 0.95, Span: sp(),
		}); err != nil {
			return err
		}
		fOprih := fileHelper(t, tx, "cfg", objModulePath("Document", "ПриходнаяНакладная"), "// приход")
		mOprih := moduleHelper(t, tx, "cfg", objModulePath("Document", "ПриходнаяНакладная"), "приходнаянакладная", "ПриходнаяНакладная", fOprih)
		sOprih := symbolHelper(t, tx, symbolSpec{
			uid: "sym-oprihodovat-tovar", componentID: "cfg", nameNorm: "оприходоватьтовар", nameDisplay: "ОприходоватьТовар",
			kind: "procedure", moduleID: mOprih, fileID: fOprih, export: false, span: sp(),
		})
		if err := tx.InsertRegisterAccess(store.RegisterAccess{
			FileID: fOprih, SymbolID: sOprih, ObjectID: oReg, RegisterNameNorm: "товарынаскладах", Mode: "movement",
			Static: true, Confidence: 0.9, Span: sp(),
		}); err != nil {
			return err
		}
		fPoluchit := fileHelper(t, tx, "cfg", commonModulePath("ОстаткиОтчёт"), "// чтение")
		mPoluchit := moduleHelper(t, tx, "cfg", commonModulePath("ОстаткиОтчёт"), "остаткиотчёт", "ОстаткиОтчёт", fPoluchit)
		sPoluchit := symbolHelper(t, tx, symbolSpec{
			uid: "sym-poluchit-ostatok", componentID: "cfg", nameNorm: "получитьостаток", nameDisplay: "ПолучитьОстаток",
			kind: "function", moduleID: mPoluchit, fileID: fPoluchit, export: false, span: sp(),
		})
		if err := tx.InsertRegisterAccess(store.RegisterAccess{
			FileID: fPoluchit, SymbolID: sPoluchit, ObjectID: oReg, RegisterNameNorm: "товарынаскладах", Mode: "read",
			Static: true, Confidence: 0.85, Span: sp(),
		}); err != nil {
			return err
		}

		// --- form: Document Заказ, форма ФормаДокумента, элемент Склад ---
		fZakazMeta := fileHelper(t, tx, "cfg", declPath("Document", "Заказ"), "<meta/>")
		oZakaz, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00Document\x00заказ", ComponentID: "cfg",
			MType: "Document", NameNorm: "заказ", NameDisplay: "Заказ", FileID: fZakazMeta, Layer: "base",
		})
		if err != nil {
			return err
		}
		mmSklad, err := tx.InsertMetadataMember(store.MetadataMember{
			IdentityKey: "cfg\x00object\x00Document\x00заказ\x00member\x00Attribute\x00\x00склад",
			ComponentID: "cfg", ObjectID: oZakaz, OriginFileID: fZakazMeta, Kind: "Attribute",
			NameNorm: "склад", NameDisplay: "Склад",
		})
		if err != nil {
			return err
		}
		formZakaz, err := tx.EnsureForm(store.Form{
			IdentityKey: "cfg\x00form\x00Document.Заказ.Form.ФормаДокумента", ComponentID: "cfg",
			OwnerObjectID: oZakaz, NameNorm: "формадокумента", NameDisplay: "ФормаДокумента",
		})
		if err != nil {
			return err
		}
		fFormZakaz := fileHelper(t, tx, "cfg", formModulePathOf("Document", "Заказ", "ФормаДокумента"), "// форма заказа")
		if err := tx.PutFormDeclaration(formZakaz, fFormZakaz); err != nil {
			return err
		}
		if _, err := tx.InsertFormElement(store.FormElement{
			IdentityKey: "cfg\x00formel\x00Document.Заказ.Form.ФормаДокумента.Склад", ComponentID: "cfg",
			FormID: formZakaz, OriginFileID: fFormZakaz, NameNorm: "склад", NameDisplay: "Склад",
			EType: "Field", DataPath: "Объект.Склад",
		}); err != nil {
			return err
		}
		mFormModuleZakaz := moduleHelper(t, tx, "cfg", formModulePathOf("Document", "Заказ", "ФормаДокумента"), "формадокумента", "ФормаДокумента", fFormZakaz)
		ids.skladPriIzmUID = "sym-sklad-pri-izm"
		sHandler := symbolHelper(t, tx, symbolSpec{
			uid: ids.skladPriIzmUID, componentID: "cfg", nameNorm: "складприизменении", nameDisplay: "СкладПриИзменении",
			kind: "procedure", moduleID: mFormModuleZakaz, fileID: fFormZakaz, export: false, span: spanOf(0, 30),
		})
		if err := tx.InsertHandlerBinding(store.HandlerBinding{
			FormID: formZakaz, Source: "склад", Event: "ПриИзменении", HandlerNameNorm: "складприизменении",
			HandlerSymbolID: sHandler, OriginFileID: fFormZakaz, Resolution: "resolved",
		}); err != nil {
			return err
		}
		fServer := fileHelper(t, tx, "cfg", commonModulePath("ЦеныСервер"), "// сервер")
		mServer := moduleHelper(t, tx, "cfg", commonModulePath("ЦеныСервер"), "ценысервер", "ЦеныСервер", fServer)
		sServer := symbolHelper(t, tx, symbolSpec{
			uid: "sym-obnovit-ceny", componentID: "cfg", nameNorm: "обновитьценынасервере", nameDisplay: "ОбновитьЦеныНаСервере",
			kind: "procedure", moduleID: mServer, fileID: fServer, export: true, span: sp(),
		})
		callHelper(t, tx, fFormZakaz, sHandler, sServer, "обновитьценынасервере")
		_ = mmSklad

		// --- add-attribute: Document ЗаказКлиента ---
		fZKMeta := fileHelper(t, tx, "cfg", declPath("Document", "ЗаказКлиента"), "<meta/>")
		oZK, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00Document\x00заказклиента", ComponentID: "cfg",
			MType: "Document", NameNorm: "заказклиента", NameDisplay: "ЗаказКлиента", FileID: fZKMeta, Layer: "base",
		})
		if err != nil {
			return err
		}
		if _, err := tx.InsertMetadataMember(store.MetadataMember{
			IdentityKey: "cfg\x00object\x00Document\x00заказклиента\x00member\x00Attribute\x00\x00комментарий",
			ComponentID: "cfg", ObjectID: oZK, OriginFileID: fZKMeta, Kind: "Attribute",
			NameNorm: "комментарий", NameDisplay: "Комментарий",
		}); err != nil {
			return err
		}
		if _, err := tx.InsertMetadataMember(store.MetadataMember{
			IdentityKey: "cfg\x00object\x00Document\x00заказклиента\x00member\x00Attribute\x00\x00организация",
			ComponentID: "cfg", ObjectID: oZK, OriginFileID: fZKMeta, Kind: "Attribute",
			NameNorm: "организация", NameDisplay: "Организация",
		}); err != nil {
			return err
		}
		formZK, err := tx.EnsureForm(store.Form{
			IdentityKey: "cfg\x00form\x00Document.ЗаказКлиента.Form.ФормаДокумента", ComponentID: "cfg",
			OwnerObjectID: oZK, NameNorm: "формадокумента", NameDisplay: "ФормаДокумента",
		})
		if err != nil {
			return err
		}
		// PutFormDeclaration обязателен: reconcile (orphan-sweep, store.Write)
		// удаляет form-узлы, не привязанные ни к одному source_file через
		// form_declaration — тот же приём, что buildImpactFixture в
		// internal/app/impact_test.go.
		if err := tx.PutFormDeclaration(formZK, fZKMeta); err != nil {
			return err
		}
		roleID, err := tx.EnsureRole(store.Role{ComponentID: "cfg", NameNorm: "менеджер", NameDisplay: "Менеджер", FileID: fZKMeta, Layer: "base"})
		if err != nil {
			return err
		}
		if err := tx.InsertRoleRight(store.RoleRight{
			RoleID: roleID, ObjectID: oZK, ObjectNameNorm: "document.заказклиента", RightName: "Чтение",
			Value: true, SetForNewObject: false, OriginFileID: fZKMeta,
		}); err != nil {
			return err
		}
		fPrint := fileHelper(t, tx, "cfg", commonModulePath("ПечатьЗаказов"), "// печать")
		mPrint := moduleHelper(t, tx, "cfg", commonModulePath("ПечатьЗаказов"), "печатьзаказов", "ПечатьЗаказов", fPrint)
		sPrint := symbolHelper(t, tx, symbolSpec{
			uid: "sym-pechat-zakazov", componentID: "cfg", nameNorm: "напечататьзаказ", nameDisplay: "НапечататьЗаказ",
			kind: "procedure", moduleID: mPrint, fileID: fPrint, export: true, span: sp(),
		})
		qPrintID, err := tx.InsertQuery(store.Query{
			IdentityKey: "cfg\x00query\x002", ComponentID: "cfg", SymbolID: sPrint, FileID: fPrint,
			Span: spanOf(0, 20), Staticity: "static", Text: "ВЫБРАТЬ ИЗ Документ.ЗаказКлиента",
			Confidence: 1,
		})
		if err != nil {
			return err
		}
		return tx.InsertQueryReference(store.QueryReference{
			QueryID: qPrintID, Kind: "table", NameNorm: "заказклиента", ObjectID: oZK, SpanStart: 12, SpanEnd: 30,
		})
	})
	if err != nil {
		t.Fatalf("seedScenarioFixture: %v", err)
	}
	return ids
}

// seedLongBodyFixture строит один символ "ДлинныйМетод" (component cfg) с
// телом длиннее bodyInlineCap (1500 рун) — definition обязан упасть в
// complete_via_resource (усечён, но ПОЛНОСТЬЮ представлен через resourceURI,
// не частично отдан) — плюс один caller и один callee, оба маленькие
// (complete_inline), чтобы sufficiencyStatus вышел ровно requires_resource_fetch
// (нет partial/missing, есть хотя бы один complete_via_resource).
func seedLongBodyFixture(t *testing.T, st *store.Store) (uid string) {
	t.Helper()
	ctx := context.Background()
	uid = "sym-long-method"
	err := st.Write(ctx, func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		body := "Процедура ДлинныйМетод() Экспорт\n"
		for len([]rune(body)) < bodyInlineCap+200 {
			body += "\t// строка заполнения тела ради проверки усечения и resource-ссылки\n"
		}
		body += "КонецПроцедуры"
		fLong := fileHelper(t, tx, "cfg", commonModulePath("ДлинныйМодуль"), body)
		mLong := moduleHelper(t, tx, "cfg", commonModulePath("ДлинныйМодуль"), "длинныймодуль", "ДлинныйМодуль", fLong)
		sLong := symbolHelper(t, tx, symbolSpec{
			uid: uid, componentID: "cfg", nameNorm: "длинныйметод", nameDisplay: "ДлинныйМетод",
			kind: "procedure", moduleID: mLong, fileID: fLong, export: true, span: spanOf(0, len(body)),
		})

		fCallee := fileHelper(t, tx, "cfg", commonModulePath("Мелочи"), "// callee")
		mCallee := moduleHelper(t, tx, "cfg", commonModulePath("Мелочи"), "мелочи", "Мелочи", fCallee)
		sCallee := symbolHelper(t, tx, symbolSpec{
			uid: "sym-korotkij-vyzov", componentID: "cfg", nameNorm: "короткийвызов", nameDisplay: "КороткийВызов",
			kind: "procedure", moduleID: mCallee, fileID: fCallee, export: false, span: sp(),
		})
		callHelper(t, tx, fLong, sLong, sCallee, "короткийвызов")

		fCaller := fileHelper(t, tx, "cfg", commonModulePath("Вызывающий"), "// caller")
		mCaller := moduleHelper(t, tx, "cfg", commonModulePath("Вызывающий"), "вызывающий", "Вызывающий", fCaller)
		sCaller := symbolHelper(t, tx, symbolSpec{
			uid: "sym-vyzyvaet-dlinnyj", componentID: "cfg", nameNorm: "вызываетдлинный", nameDisplay: "ВызываетДлинный",
			kind: "procedure", moduleID: mCaller, fileID: fCaller, export: true, span: sp(),
		})
		callHelper(t, tx, fCaller, sCaller, sLong, "длинныйметод")
		return nil
	})
	if err != nil {
		t.Fatalf("seedLongBodyFixture: %v", err)
	}
	return uid
}

// seedPartialRegisterFixture строит регистр "Рег" (component cfg) с двумя
// доступами write/movement. Модули лежат по РЕАЛЬНОЙ раскладке
// выгрузки (commonModulePath), а не под именами "M1"/"M2": ownerDisplay
// кандидата — путь модуля, поэтому фиктивно короткий путь
// одновременно подделывал и раскладку, и арифметику бюджета.
//
// charCost кандидата writes_movements в expand2.go (expandRegister) —
// runeLen(label)+runeLen(obj.NameDisplay)+4, где label =
// "<mode>: <module>.<symbol>", а module = "CommonModules/М1/Ext/Module.bsl"
// (31 руна): "write: <31>.A" = 40 рун + "Рег" (3) + 4 = 47;
// "movement: <31>.B" = 43 + 3 + 4 = 50. При равных остальных множителях
// формулы (тот же anchor/depth/component/confidence=1 у обоих) score обратно
// пропорционален charCost, поэтому дешёвый write (47) всегда обгоняет
// movement (50). budgetChars=47 вмещает РОВНО write (47<=47, остаток 0 <
// 50) — writes_movements обязана выйти partial (returned=1, total=2).
func seedPartialRegisterFixture(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	err := st.Write(ctx, func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		fMeta := fileHelper(t, tx, "cfg", declPath("InformationRegister", "Рег"), "<meta/>")
		oReg, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00InformationRegister\x00рег", ComponentID: "cfg",
			MType: "InformationRegister", NameNorm: "рег", NameDisplay: "Рег", FileID: fMeta, Layer: "base",
		})
		if err != nil {
			return err
		}
		fA := fileHelper(t, tx, "cfg", commonModulePath("М1"), "// A")
		mA := moduleHelper(t, tx, "cfg", commonModulePath("М1"), "м1", "М1", fA)
		sA := symbolHelper(t, tx, symbolSpec{uid: "sym-a", componentID: "cfg", nameNorm: "a", nameDisplay: "A", kind: "procedure", moduleID: mA, fileID: fA, span: sp()})
		if err := tx.InsertRegisterAccess(store.RegisterAccess{
			FileID: fA, SymbolID: sA, ObjectID: oReg, RegisterNameNorm: "рег", Mode: "write", Static: true, Confidence: 1, Span: sp(),
		}); err != nil {
			return err
		}
		fB := fileHelper(t, tx, "cfg", commonModulePath("М2"), "// B")
		mB := moduleHelper(t, tx, "cfg", commonModulePath("М2"), "м2", "М2", fB)
		sB := symbolHelper(t, tx, symbolSpec{uid: "sym-b", componentID: "cfg", nameNorm: "b", nameDisplay: "B", kind: "procedure", moduleID: mB, fileID: fB, span: sp()})
		return tx.InsertRegisterAccess(store.RegisterAccess{
			FileID: fB, SymbolID: sB, ObjectID: oReg, RegisterNameNorm: "рег", Mode: "movement", Static: true, Confidence: 1, Span: sp(),
		})
	})
	if err != nil {
		t.Fatalf("seedPartialRegisterFixture: %v", err)
	}
}
