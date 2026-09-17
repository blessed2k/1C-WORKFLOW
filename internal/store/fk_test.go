package store

import (
	"context"
	"strings"
	"testing"
)

// Обязательный FK-тест (а) раздела 15: ОДИН DELETE FROM source_file для XML
// объекта с реквизитами и вложенными реквизитами проходит при foreign_keys=ON,
// не зависит от внутреннего порядка каскадов и после reconciliation не оставляет
// строк ни в metadata_object, ни в metadata_member, ни orphan-строк в node.
func TestDeleteObjectXMLCascade(t *testing.T) {
	s, f := seeded(t)
	ctx := context.Background()
	if err := s.Write(ctx, func(tx *WriteTx) error {
		return tx.DeleteSourceFiles(f.fileObjectXML)
	}); err != nil {
		t.Fatalf("удаление XML объекта: %v", err)
	}
	assertValid(t, s, "после удаления XML объекта")

	for _, table := range []string{"metadata_object", "metadata_member", "role", "role_right",
		"event_subscription", "scheduled_job"} {
		if n := countRows(t, s, table, ""); n != 0 {
			t.Errorf("%s: осталось %d строк", table, n)
		}
	}
	if n := countRows(t, s, "node", "kind IN ('metadata_object','metadata_member')"); n != 0 {
		t.Errorf("orphan node метаданных: %d", n)
	}
	// Форма жива: остался аспект form_structure из Form.xml, который не менялся.
	if n := countRows(t, s, "form", "id=?", f.formID); n != 1 {
		t.Error("форма исчезла, хотя Form.xml не менялся")
	}
	if n := countRows(t, s, "form_declaration", "form_id=?", f.formID); n != 0 {
		t.Error("аспект form_declaration не удалён вместе со своим файлом")
	}
	// Ссылка на объект жила в Module.bsl и обязана была стать unresolved (шаг 1b),
	// а не потерять цель молча.
	if n := countRows(t, s, "reference", "id=? AND resolution='unresolved'", f.refResolvedMetadata); n != 1 {
		t.Error("ссылка на удалённый объект не переведена в unresolved")
	}
}

// Обязательный FK-тест (б) раздела 15: удаление Module.bsl с symbols, query и
// register_access после reconciliation не оставляет orphan node.
func TestDeleteModuleBSLCascade(t *testing.T) {
	s, f := seeded(t)
	if err := s.Write(context.Background(), func(tx *WriteTx) error {
		return tx.DeleteSourceFiles(f.fileModuleBSL)
	}); err != nil {
		t.Fatalf("удаление Module.bsl: %v", err)
	}
	assertValid(t, s, "после удаления Module.bsl")

	for _, table := range []string{"symbol", "parameter", "query", "query_reference",
		"register_access", "reference", "reference_candidate", "resolution_dep", "call_edge"} {
		if n := countRows(t, s, table, ""); n != 0 {
			t.Errorf("%s: осталось %d строк", table, n)
		}
	}
	if n := countRows(t, s, "node", "kind IN ('symbol','query')"); n != 0 {
		t.Errorf("orphan node символов и запросов: %d", n)
	}
	if n := countRows(t, s, "fts_symbols", ""); n != 0 {
		t.Errorf("строки FTS пережили удаление своих символов: %d", n)
	}
	// Модуль остался: аспект module_context из X.xml не тронут.
	if n := countRows(t, s, "module", "id=?", f.moduleID); n != 1 {
		t.Error("модуль исчез, хотя X.xml не менялся")
	}
	if n := countRows(t, s, "module_code", "module_id=?", f.moduleID); n != 0 {
		t.Error("аспект module_code не удалён вместе со своим файлом")
	}
}

// Удаление ОБОИХ источников составной identity убирает и её саму: identity
// живёт, пока есть хоть один аспект, и ни секундой дольше.
func TestDeleteBothAspectsRemovesIdentity(t *testing.T) {
	s, f := seeded(t)
	if err := s.Write(context.Background(), func(tx *WriteTx) error {
		return tx.DeleteSourceFiles(f.fileModuleBSL, f.fileModuleXML, f.fileObjectXML, f.fileFormXML)
	}); err != nil {
		t.Fatalf("удаление всех файлов: %v", err)
	}
	assertValid(t, s, "после удаления всех файлов")
	for _, table := range []string{"node", "module", "form", "symbol"} {
		if n := countRows(t, s, table, ""); n != 0 {
			t.Errorf("%s: осталось %d строк без единого источника", table, n)
		}
	}
}

// Закрепляющий тест раздела 15 (ревью №4): изменён CommonModules/X.xml,
// Module.bsl не изменён -> uid и id всех symbols сохраняются, references на них
// не трогаются, module_context.props обновлён.
func TestXMLChangeKeepsSymbolIdentity(t *testing.T) {
	s, f := seeded(t)
	ctx := context.Background()
	refsBefore := countRows(t, s, "reference", "target_symbol_id=?", f.symA)

	var uidBefore, uidAfter, props string
	if err := s.Read(ctx, func(tx *ReadTx) error {
		var err error
		uidBefore, err = tx.c.queryText(tx.ctx, `SELECT uid FROM symbol WHERE id=?`, f.symA)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Инкремент по XML-файлу: файл и его аспект удаляются и вставляются заново,
	// identity модуля находится по тому же identity_key.
	if err := s.Write(ctx, func(tx *WriteTx) error {
		if err := tx.DeleteSourceFiles(f.fileModuleXML); err != nil {
			return err
		}
		hash, err := tx.PutBlob([]byte("<Module Global=\"true\"/>"))
		if err != nil {
			return err
		}
		fileID, err := tx.InsertSourceFile(SourceFile{
			ComponentID: fxComponent, RelPath: "CommonModules/X.xml", Size: 23,
			MtimeNS: 2, ContentHash: hash, ParserVersion: 1,
		})
		if err != nil {
			return err
		}
		moduleID, err := tx.EnsureModule(Module{
			IdentityKey: "module:cfg:CommonModules/X", ComponentID: fxComponent,
			Kind: "common", NameNorm: "x", NameDisplay: "X",
		})
		if err != nil {
			return err
		}
		if moduleID != f.moduleID {
			t.Errorf("identity модуля не переиспользована: было %d, стало %d", f.moduleID, moduleID)
		}
		return tx.PutModuleContext(moduleID, fileID, `{"Server":true,"Global":true}`)
	}); err != nil {
		t.Fatalf("правка X.xml: %v", err)
	}
	assertValid(t, s, "после правки X.xml")

	if err := s.Read(ctx, func(tx *ReadTx) error {
		var err error
		if uidAfter, err = tx.c.queryText(tx.ctx, `SELECT uid FROM symbol WHERE id=?`, f.symA); err != nil {
			return err
		}
		props, err = tx.c.queryText(tx.ctx, `SELECT props FROM module_context WHERE module_id=?`, f.moduleID)
		return err
	}); err != nil {
		t.Fatalf("символ исчез после правки XML: %v", err)
	}
	if uidAfter != uidBefore {
		t.Errorf("uid символа изменился: было %q, стало %q", uidBefore, uidAfter)
	}
	if n := countRows(t, s, "reference", "target_symbol_id=?", f.symA); n != refsBefore {
		t.Errorf("references на символ изменились: было %d, стало %d", refsBefore, n)
	}
	if !strings.Contains(props, "Global") {
		t.Errorf("module_context.props не обновлён: %q", props)
	}
}

// Ключевая проверка порядка транзакции (ревью №5, шаг 1b): каскадный SET NULL
// на resolved-строке обязан ГРОМКО валить транзакцию через CHECK, а не молча
// портить XOR-автомат. Это архитектурное утверждение о поведении SQLite,
// поэтому проверяется экспериментом: удаление файла БЕЗ шага (1b) обязано
// упасть, а DeleteSourceFiles, который делает (1b) сам, — пройти.
func TestSetNullOnResolvedViolatesCheck(t *testing.T) {
	s, f := seeded(t)
	ctx := context.Background()

	err := s.Write(ctx, func(tx *WriteTx) error {
		// Ссылка живёт в Module.bsl, её цель — объект из Catalogs/Y.xml.
		// Удаляем XML владельца в обход шага (1b).
		return tx.c.exec(tx.ctx, `DELETE FROM source_file WHERE id=?`, f.fileObjectXML)
	})
	if err == nil {
		t.Fatal("удаление цели без шага (1b) прошло молча: XOR-автомат не защищён CHECK-ом")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "check") {
		t.Errorf("ошибка не похожа на нарушение CHECK: %v", err)
	}
	// Транзакция откатилась целиком: индекс цел.
	assertValid(t, s, "после отката попытки без (1b)")
	if n := countRows(t, s, "metadata_object", "id=?", f.objectID); n != 1 {
		t.Error("откат не вернул объект метаданных")
	}

	if err := s.Write(ctx, func(tx *WriteTx) error {
		return tx.DeleteSourceFiles(f.fileObjectXML)
	}); err != nil {
		t.Fatalf("правильный порядок (1b) -> (2) не прошёл: %v", err)
	}
	assertValid(t, s, "после правильного порядка")
}

// CHECK-и XOR-автомата отвергают некорректные комбинации на вставке. Список
// комбинаций — из раздела 15: у resolved ровно одна цель своего класса, у
// остальных состояний целей нет вовсе.
func TestReferenceXORChecks(t *testing.T) {
	s, f := seeded(t)
	cases := []struct {
		name string
		ref  Reference
	}{
		{"resolved без target_class", Reference{Resolution: "resolved", TargetSymbolID: f.symA}},
		{"unresolved с целью", Reference{Resolution: "unresolved", TargetSymbolID: f.symA}},
		{"resolved+symbol без цели", Reference{Resolution: "resolved", TargetClass: "symbol"}},
		{"resolved+symbol с двумя целями", Reference{Resolution: "resolved", TargetClass: "symbol",
			TargetSymbolID: f.symA, TargetObjectID: f.objectID}},
		{"resolved+platform без ключа", Reference{Resolution: "resolved", TargetClass: "platform"}},
		{"dynamic с target_class", Reference{Resolution: "dynamic", TargetClass: "symbol", TargetSymbolID: f.symA}},
		{"неизвестное resolution", Reference{Resolution: "guessed", TargetClass: "symbol", TargetSymbolID: f.symA}},
	}
	for _, tc := range cases {
		r := tc.ref
		r.FileID, r.Kind, r.NameNorm, r.Confidence = f.fileModuleBSL, "call", "н", 1
		err := s.Write(context.Background(), func(tx *WriteTx) error {
			_, err := tx.InsertReference(r)
			return err
		})
		if err == nil {
			t.Errorf("%s: вставка прошла, хотя CHECK обязан был её отвергнуть", tc.name)
		}
	}
	assertValid(t, s, "после попыток нарушить XOR")
}

// Режим доступа к регистру — закрытое множество из брифа (write|read|movement|
// clear). Опечатка в режиме означала бы find_register_writes, который молча не
// находит записи.
func TestRegisterAccessModeIsClosedSet(t *testing.T) {
	s, f := seeded(t)
	err := s.Write(context.Background(), func(tx *WriteTx) error {
		return tx.InsertRegisterAccess(RegisterAccess{
			FileID: f.fileModuleBSL, RegisterNameNorm: "регистр", Mode: "записать",
			Confidence: 1,
		})
	})
	if err == nil {
		t.Fatal("неизвестный режим доступа к регистру принят")
	}
}
