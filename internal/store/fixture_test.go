package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// fixture — маленький, но структурно полный набор данных: составные identity
// (модуль из XML+BSL, форма из объявления и Form.xml), символы, ссылки всех
// классов разрешения, кандидаты, resolution_dep, call_edge, запрос с
// использованием таблицы, доступ к регистру, объект метаданных с вложенными
// реквизитами, обработчик формы, подписка, задание, роль с правом.
type fixture struct {
	fileModuleXML, fileModuleBSL int64
	fileObjectXML, fileFormXML   int64
	hashObjectXML                string
	moduleID                     int64
	symA, symB                   int64
	objectID, memberID           int64
	formID, elementID            int64
	queryID                      int64
	roleID                       int64
	refResolvedSymbol            int64
	refResolvedMetadata          int64
}

const fxComponent = "cfg"

// seedFixture наполняет индекс через типизированный интерфейс WriteTx: тест
// пользуется тем же API, что и остальные модули, а не собственным SQL.
func seedFixture(tx *WriteTx, out *fixture) error {
	f := fixture{}
	if err := tx.UpsertComponent(Component{
		ID: fxComponent, Kind: "configuration", Root: ".", Display: "фикстура",
	}); err != nil {
		return err
	}

	file := func(path string, body string) (int64, string, error) {
		hash, err := tx.PutBlob([]byte(body))
		if err != nil {
			return 0, "", err
		}
		id, err := tx.InsertSourceFile(SourceFile{
			ComponentID: fxComponent, RelPath: path, Size: int64(len(body)),
			MtimeNS: 1, ContentHash: hash, ParserVersion: 1,
		})
		return id, hash, err
	}

	var err error
	if f.fileModuleXML, _, err = file("CommonModules/X.xml", "<Module/>"); err != nil {
		return err
	}
	if f.fileModuleBSL, _, err = file("CommonModules/X/Ext/Module.bsl", "Функция А() Экспорт КонецФункции"); err != nil {
		return err
	}
	if f.fileObjectXML, f.hashObjectXML, err = file("Catalogs/Y.xml", "<Catalog/>"); err != nil {
		return err
	}
	if f.fileFormXML, _, err = file("Catalogs/Y/Forms/ФормаЭлемента/Ext/Form.xml", "<Form/>"); err != nil {
		return err
	}

	// Модуль: identity + два аспекта в разных файлах.
	if f.moduleID, err = tx.EnsureModule(Module{
		IdentityKey: "module:cfg:CommonModules/X", ComponentID: fxComponent,
		Kind: "common", NameNorm: "x", NameDisplay: "X",
	}); err != nil {
		return err
	}
	if err := tx.PutModuleContext(f.moduleID, f.fileModuleXML, `{"Server":true}`); err != nil {
		return err
	}
	if err := tx.PutModuleCode(f.moduleID, f.fileModuleBSL); err != nil {
		return err
	}

	// isExport различается намеренно: фикстура обязана содержать и экспортный,
	// и не-экспортный символ — иначе проверки, которые опираются на это поле
	// (бэкфилл миграции, диагностика not-exported), верны при любом коде.
	sym := func(name, disp string, isExport bool) (int64, error) {
		id, err := tx.InsertSymbol(Symbol{
			IdentityKey: "symbol:cfg:CommonModules/X/" + name, ComponentID: fxComponent,
			UID: "uid-" + name, ModuleID: f.moduleID, OriginFileID: f.fileModuleBSL,
			Kind: "function", NameNorm: name, NameDisplay: disp, IsExport: isExport,
			Directive: "&НаСервере",
			Span:      domain.Span{StartByte: 0, EndByte: 10, StartLine: 1, StartCol: 1, EndLine: 2, EndCol: 14},
			Signature: "Функция " + disp + "() Экспорт", DocFirstLine: "// описание",
			Region: "ПрограммныйИнтерфейс",
		})
		if err != nil {
			return 0, err
		}
		return id, tx.InsertParameter(id, Parameter{Ord: 0, Name: "Параметр1", DefaultExpr: "Неопределено"})
	}
	if f.symA, err = sym("а", "А", true); err != nil {
		return err
	}
	if f.symB, err = sym("б", "Б", false); err != nil {
		return err
	}

	// Объект метаданных с реквизитом и вложенным реквизитом табличной части.
	if f.objectID, err = tx.EnsureMetadataObject(MetadataObject{
		IdentityKey: "metadata_object:cfg:Catalogs/Y", ComponentID: fxComponent,
		UUID: "uuid", MType: "Catalog", NameNorm: "y", NameDisplay: "Y",
		FileID: f.fileObjectXML, PropsJSON: "{}",
	}); err != nil {
		return err
	}
	if f.memberID, err = tx.InsertMetadataMember(MetadataMember{
		IdentityKey: "metadata_member:cfg:Catalogs/Y/р", ComponentID: fxComponent,
		ObjectID: f.objectID, OriginFileID: f.fileObjectXML, Kind: "attribute",
		NameNorm: "р", NameDisplay: "Р", TypesJSON: "[]",
	}); err != nil {
		return err
	}
	if _, err = tx.InsertMetadataMember(MetadataMember{
		IdentityKey: "metadata_member:cfg:Catalogs/Y/тч/вложенный", ComponentID: fxComponent,
		ObjectID: f.objectID, OriginFileID: f.fileObjectXML, Kind: "attribute",
		NameNorm: "вложенный", NameDisplay: "Вложенный", ParentMember: f.memberID,
	}); err != nil {
		return err
	}

	// Форма: identity + объявление в XML владельца + структура из Form.xml.
	if f.formID, err = tx.EnsureForm(Form{
		IdentityKey: "form:cfg:Catalogs/Y/Forms/ФормаЭлемента", ComponentID: fxComponent,
		OwnerObjectID: f.objectID, NameNorm: "формаэлемента", NameDisplay: "ФормаЭлемента",
	}); err != nil {
		return err
	}
	if err := tx.PutFormDeclaration(f.formID, f.fileObjectXML); err != nil {
		return err
	}
	if err := tx.PutFormStructure(f.formID, f.fileFormXML); err != nil {
		return err
	}
	if f.elementID, err = tx.InsertFormElement(FormElement{
		IdentityKey: "form_element:cfg:Y/ФормаЭлемента/Поле", ComponentID: fxComponent,
		FormID: f.formID, OriginFileID: f.fileFormXML, NameNorm: "поле",
		NameDisplay: "Поле", EType: "InputField", DataPath: "Объект.Р",
	}); err != nil {
		return err
	}
	if _, err = tx.InsertFormCommand(FormCommand{
		IdentityKey: "form_command:cfg:Y/ФормаЭлемента/Провести", ComponentID: fxComponent,
		FormID: f.formID, OriginFileID: f.fileFormXML, NameNorm: "провести",
		NameDisplay: "Провести", ActionNorm: "провестикоманда",
	}); err != nil {
		return err
	}
	if err := tx.InsertHandlerBinding(HandlerBinding{
		FormID: f.formID, Source: "форма", Event: "ПриОткрытии",
		HandlerNameNorm: "приоткрытии", OriginFileID: f.fileFormXML, Resolution: "unresolved",
	}); err != nil {
		return err
	}

	// Запрос и доступ к регистру внутри BSL.
	if f.queryID, err = tx.InsertQuery(Query{
		IdentityKey: "query:cfg:CommonModules/X/а/1", ComponentID: fxComponent,
		SymbolID: f.symA, FileID: f.fileModuleBSL,
		Span: domain.Span{StartByte: 0, EndByte: 10}, Staticity: "static",
		Text: "ВЫБРАТЬ 1", Confidence: 1,
	}); err != nil {
		return err
	}
	if err := tx.InsertQueryReference(QueryReference{
		QueryID: f.queryID, Kind: "table", NameNorm: "справочник.y", ObjectID: f.objectID,
	}); err != nil {
		return err
	}
	inTx := true
	if err := tx.InsertRegisterAccess(RegisterAccess{
		FileID: f.fileModuleBSL, SymbolID: f.symA, ObjectID: f.objectID,
		RegisterNameNorm: "регистр", Mode: "write", InTransaction: &inTx,
		Static: true, Confidence: 0.9, Span: domain.Span{StartByte: 0, EndByte: 10},
	}); err != nil {
		return err
	}

	// Ссылки: resolved->symbol, resolved->metadata, resolved->platform,
	// ambiguous с двумя кандидатами, dynamic.
	base := Reference{
		FileID: f.fileModuleBSL, FromSymbolID: f.symB, Kind: "call", Confidence: 1,
		Span: domain.Span{StartByte: 0, EndByte: 5, StartLine: 1, StartCol: 1},
	}
	r := base
	r.NameNorm, r.QualifierNorm = "а", "x"
	r.Resolution, r.TargetClass, r.TargetSymbolID = "resolved", "symbol", f.symA
	if f.refResolvedSymbol, err = tx.InsertReference(r); err != nil {
		return err
	}
	r = base
	r.NameNorm, r.QualifierNorm = "y", "справочники"
	r.Resolution, r.TargetClass, r.TargetObjectID = "resolved", "metadata", f.objectID
	if f.refResolvedMetadata, err = tx.InsertReference(r); err != nil {
		return err
	}
	r = base
	r.NameNorm = "стрнайти"
	r.Resolution, r.TargetClass, r.PlatformKey = "resolved", "platform", "ГлобальныйКонтекст.СтрНайти"
	refPlatform, err := tx.InsertReference(r)
	if err != nil {
		return err
	}
	r = base
	r.NameNorm, r.Resolution = "неоднозначный", "ambiguous"
	refAmbiguous, err := tx.InsertReference(r)
	if err != nil {
		return err
	}
	for i, target := range []int64{f.symA, f.symB} {
		if err := tx.InsertReferenceCandidate(refAmbiguous, target, i, "global-module"); err != nil {
			return err
		}
	}
	r = base
	r.NameNorm, r.Resolution = "динамический", "dynamic"
	refDynamic, err := tx.InsertReference(r)
	if err != nil {
		return err
	}

	for _, id := range []int64{f.refResolvedSymbol, f.refResolvedMetadata, refPlatform, refAmbiguous, refDynamic} {
		if err := tx.InsertResolutionDep(fmt.Sprintf("key-%d", id), id); err != nil {
			return err
		}
		// Негативные lookup-ы регистрируются наравне с положительными (18.4).
		if err := tx.InsertResolutionDep("key-global", id); err != nil {
			return err
		}
	}
	if err := tx.InsertCallEdge(CallEdge{
		CallerID: f.symB, CalleeID: f.symA, CalleeNameNorm: "а", QualifierNorm: "x",
		Kind: "common-module", Resolution: "resolved", Confidence: 1, RefID: f.refResolvedSymbol,
	}); err != nil {
		return err
	}
	if err := tx.InsertCallEdge(CallEdge{
		CallerID: f.symB, CalleeNameNorm: "стрнайти", Kind: "platform",
		Resolution: "resolved", Confidence: 1, RefID: refPlatform,
	}); err != nil {
		return err
	}

	// Подписка, задание, роль с правом, generic-связь и диагностика.
	if err := tx.InsertEventSubscription(EventSubscription{
		ComponentID: fxComponent, NameNorm: "приз", NameDisplay: "ПриЗаписи",
		SourceKind: "defined-type", SourceNameNorm: "определяемыйтип.ссылка",
		Event: "ПриЗаписи", HandlerNameNorm: "а", HandlerSymbolID: f.symA,
		OriginFileID: f.fileObjectXML, Resolution: "resolved",
	}); err != nil {
		return err
	}
	if err := tx.InsertScheduledJob(ScheduledJob{
		ComponentID: fxComponent, NameNorm: "обмен", NameDisplay: "Обмен",
		MethodNameNorm: "а", HandlerSymbolID: f.symA, OriginFileID: f.fileObjectXML,
		Use: true, Predefined: true, Resolution: "resolved",
	}); err != nil {
		return err
	}
	if f.roleID, err = tx.EnsureRole(Role{
		ComponentID: fxComponent, NameNorm: "полныеправа", NameDisplay: "ПолныеПрава",
		FileID: f.fileObjectXML,
	}); err != nil {
		return err
	}
	if err := tx.InsertRoleRight(RoleRight{
		RoleID: f.roleID, ObjectID: f.objectID, ObjectNameNorm: "справочник.y",
		RightName: "Read", Value: true, SetForNewObject: true, OriginFileID: f.fileObjectXML,
	}); err != nil {
		return err
	}
	if err := tx.InsertDependencyEdge(DependencyEdge{
		Kind: "subsystem-contains", FromNode: f.objectID, ToNode: f.symA,
		OriginFileID: f.fileObjectXML, Confidence: 1,
	}); err != nil {
		return err
	}
	if err := tx.InsertDiagnostic(Diagnostic{
		FileID: f.fileModuleBSL, ComponentID: fxComponent, Severity: "warning",
		Code: "not-exported", Message: "вызов неэкспортного метода",
	}); err != nil {
		return err
	}

	*out = f
	return nil
}

// seeded наполняет новое хранилище фикстурой и возвращает его вместе с id.
func seeded(t *testing.T) (*Store, fixture) {
	t.Helper()
	s := openTestStore(t, Options{})
	var f fixture
	if err := s.Write(context.Background(), func(tx *WriteTx) error { return seedFixture(tx, &f) }); err != nil {
		t.Fatalf("наполнение фикстуры: %v", err)
	}
	return s, f
}

// countRows — вспомогательный счётчик для утверждений тестов.
func countRows(t *testing.T, s *Store, table string, where string, args ...any) int64 {
	t.Helper()
	var n int64
	q := `SELECT COUNT(*) FROM ` + table
	if where != "" {
		q += " WHERE " + where
	}
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		var err error
		n, err = tx.c.queryInt(tx.ctx, q, args...)
		return err
	}); err != nil {
		t.Fatalf("подсчёт %s: %v", table, err)
	}
	return n
}
