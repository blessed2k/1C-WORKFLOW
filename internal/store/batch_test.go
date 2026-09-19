package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// TestStmtCacheReusesPreparedWithinTx: внутри одной write-транзакции тот же
// текст SQL готовится один раз (issue #3, шаг 2), а после COMMIT кэш закрыт.
func TestStmtCacheReusesPreparedWithinTx(t *testing.T) {
	s := openTestStore(t, Options{})
	ctx := context.Background()
	const n = 300
	if err := s.Write(ctx, func(tx *WriteTx) error {
		var f fixture
		if err := seedFixture(tx, &f); err != nil {
			return err
		}
		before := len(tx.c.stmts.byText)
		for i := 0; i < n; i++ {
			if _, err := tx.InsertSymbol(Symbol{
				IdentityKey: fmt.Sprintf("symbol:cfg:CommonModules/X/п%d", i), ComponentID: fxComponent,
				UID: fmt.Sprintf("uid-п%d", i), ModuleID: f.moduleID, OriginFileID: f.fileModuleBSL,
				Kind: "procedure", NameNorm: fmt.Sprintf("п%d", i), NameDisplay: fmt.Sprintf("П%d", i),
			}); err != nil {
				return err
			}
		}
		// Все тексты цикла уже подготовлены наполнением фикстуры; новым может
		// оказаться разве что полный пакет fts_symbols.
		if d := len(tx.c.stmts.byText) - before; d > 1 {
			t.Errorf("на %d одинаковых вставок подготовлено %d новых выражений, ожидалось не больше 1", n, d)
		}
		return nil
	}); err != nil {
		t.Fatalf("запись: %v", err)
	}
	if s.writer.stmts != nil {
		t.Error("кэш подготовленных выражений пережил COMMIT")
	}
	if got := countRows(t, s, "fts_symbols", ""); got != n+2 {
		t.Errorf("строк fts_symbols %d, ожидалось %d", got, n+2)
	}
}

// TestStmtCacheClosedOnRollback: ошибка вызывающего откатывает транзакцию и
// закрывает кэш, следующая запись работает с чистого листа.
func TestStmtCacheClosedOnRollback(t *testing.T) {
	s := openTestStore(t, Options{})
	boom := errors.New("отказ вызывающего")
	err := s.Write(context.Background(), func(tx *WriteTx) error {
		var f fixture
		if err := seedFixture(tx, &f); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("ошибка %v, ожидалась %v", err, boom)
	}
	if s.writer.stmts != nil {
		t.Error("кэш подготовленных выражений пережил ROLLBACK")
	}
	if got := countRows(t, s, "reference", ""); got != 0 {
		t.Errorf("после отката осталось %d ссылок", got)
	}
	if _, f := seeded(t); f.moduleID == 0 {
		t.Error("запись после отката не прошла")
	}
}

// TestBatchInsertVisibleAndOrdered: строки из буфера пакетной вставки видны
// любому чтению той же транзакции, id ссылок совпадают с тем, что лежит в
// таблице, а дочерний буфер, заполнившийся раньше родительского, не ломает
// внешний ключ (родитель уходит первым).
func TestBatchInsertVisibleAndOrdered(t *testing.T) {
	s := openTestStore(t, Options{})
	ctx := context.Background()
	const refs = 2*batchRows + 7 // два полных пакета и хвост
	ids := make([]int64, 0, refs)
	if err := s.Write(ctx, func(tx *WriteTx) error {
		var f fixture
		if err := seedFixture(tx, &f); err != nil {
			return err
		}
		for i := 0; i < refs; i++ {
			id, err := tx.InsertReference(Reference{
				FileID: f.fileModuleBSL, FromSymbolID: f.symA, Kind: "call",
				NameNorm: fmt.Sprintf("имя%d", i), Resolution: "unresolved", Confidence: 0.5,
				Span: domain.Span{StartByte: i, EndByte: i + 1, StartLine: 1, StartCol: 1},
			})
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		// Чтение той же транзакции видит и хвост буфера, ещё не ушедший пакетом.
		if _, err := tx.Meta(metaEpoch); err != nil {
			return err
		}
		n, err := tx.c.queryInt(tx.ctx, `SELECT COUNT(*) FROM reference WHERE name_norm LIKE 'имя%'`)
		if err != nil {
			return err
		}
		if n != refs {
			t.Errorf("чтение в транзакции видит %d ссылок, ожидалось %d", n, refs)
		}
		// Новая ссылка лежит в буфере, а зависимостей у неё больше полного
		// пакета: дочерний буфер сбрасывается раньше, чем родительский
		// заполнится, и родитель обязан уйти первым.
		last, err := tx.InsertReference(Reference{
			FileID: f.fileModuleBSL, FromSymbolID: f.symA, Kind: "call",
			NameNorm: "последняя", Resolution: "unresolved", Confidence: 0.5,
		})
		if err != nil {
			return err
		}
		for k := 0; k < batchRows+3; k++ {
			if err := tx.InsertResolutionDep(fmt.Sprintf("ключ%d", k), last); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("запись: %v", err)
	}
	for i, id := range ids {
		if got := countRows(t, s, "reference", "id=? AND name_norm=?", id, fmt.Sprintf("имя%d", i)); got != 1 {
			t.Fatalf("ссылка %d: id %d не совпал с таблицей", i, id)
		}
	}
	if got := countRows(t, s, "resolution_dep", "ref_id=(SELECT id FROM reference WHERE name_norm='последняя')"); got != batchRows+3 {
		t.Errorf("зависимостей %d, ожидалось %d", got, batchRows+3)
	}
	assertValid(t, s, "после пакетной вставки")
}

// TestBatchInsertForeignKeyFailureRollsBack: нарушение внешнего ключа в строке
// буфера всплывает ошибкой той же транзакции и откатывает её целиком, а не
// теряется молча.
func TestBatchInsertForeignKeyFailureRollsBack(t *testing.T) {
	s := openTestStore(t, Options{})
	err := s.Write(context.Background(), func(tx *WriteTx) error {
		var f fixture
		if err := seedFixture(tx, &f); err != nil {
			return err
		}
		return tx.InsertParameter(987654321, Parameter{Ord: 0, Name: "Висячий"})
	})
	if err == nil {
		t.Fatal("параметр несуществующего символа принят")
	}
	if got := countRows(t, s, "symbol", ""); got != 0 {
		t.Errorf("после отката осталось %d символов", got)
	}
}

// TestBatchSpecsMatchSchema: колонки каждого пакета сверяются со схемой по
// именам. Каждая колонка пакета есть в таблице, а колонка таблицы, которой
// нет в пакете, обязана быть необязательной (NULL, умолчание или rowid): иначе
// пакет молча писал бы строку без неё.
func TestBatchSpecsMatchSchema(t *testing.T) {
	s := openTestStore(t, Options{})
	b := newTxBatches()
	if len(b.all) != 12 {
		t.Fatalf("буферов %d, ожидалось 12", len(b.all))
	}
	for _, x := range b.all {
		table := x.tableName()
		type colInfo struct {
			notNull, hasDefault, pk bool
		}
		cols := map[string]colInfo{}
		if err := s.Read(context.Background(), func(tx *ReadTx) error {
			rows, err := tx.c.query(tx.ctx, `SELECT name, "notnull", dflt_value IS NOT NULL, pk FROM pragma_table_info(?)`, table)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var name string
				var ci colInfo
				var pk int
				if err := rows.Scan(&name, &ci.notNull, &ci.hasDefault, &pk); err != nil {
					return err
				}
				ci.pk = pk > 0
				cols[name] = ci
			}
			return rows.Err()
		}); err != nil {
			t.Fatal(err)
		}
		if len(cols) == 0 {
			t.Fatalf("таблица %s не найдена в схеме", table)
		}
		inSpec := map[string]bool{}
		for _, c := range x.columns() {
			inSpec[c] = true
			if _, ok := cols[c]; !ok && c != "rowid" {
				t.Errorf("%s: колонки %q нет в схеме", table, c)
			}
		}
		for name, ci := range cols {
			if inSpec[name] || !ci.notNull || ci.hasDefault || ci.pk {
				continue
			}
			t.Errorf("%s: обязательная колонка %q не пишется пакетом", table, name)
		}
	}
}

// TestReferenceIDAllocationGuard: refIDs выдаёт id строк reference как
// MAX(id)+1, и это верно ровно при двух условиях, которые здесь и держатся:
// у reference нет AUTOINCREMENT (иначе SQLite вёл бы sqlite_sequence и не
// переиспользовал бы id), и в reference вставляет только InsertReference.
func TestReferenceIDAllocationGuard(t *testing.T) {
	s := openTestStore(t, Options{})
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		ddl, err := tx.c.queryText(tx.ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='reference'`)
		if err != nil {
			return err
		}
		if !strings.Contains(ddl, "id INTEGER PRIMARY KEY") || strings.Contains(strings.ToUpper(ddl), "AUTOINCREMENT") {
			t.Errorf("reference.id больше не INTEGER PRIMARY KEY без AUTOINCREMENT:\n%s", ddl)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	insertInto := regexp.MustCompile(`(?i)INSERT\s+(OR\s+\w+\s+)?INTO\s+reference\s*\(`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if insertInto.Match(data) {
			t.Errorf("%s: прямой INSERT в reference мимо InsertReference (refIDs разойдётся с таблицей)", f)
		}
	}
	// Единственный путь вставки: описание пакета reference, из которого
	// строится INSERT (batch.go).
	if referenceSpec.table != "reference" || referenceSpec.cols[0].name != "id" {
		t.Error("пакет reference больше не пишет id явно")
	}
}

// TestBatchFlushCancelledContext: отмена контекста посреди транзакции, когда
// в буфере лежит почти полный пакет. Сброс хвоста в runWriteTx падает ошибкой отмены,
// транзакция откатывается целиком (ни строк из пакета, ни символов до него),
// кэш выражений закрыт, и следующая запись идёт с чистого листа.
func TestBatchFlushCancelledContext(t *testing.T) {
	s, f := seeded(t)
	before := countRows(t, s, "reference", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := s.Write(ctx, func(tx *WriteTx) error {
		if _, err := tx.InsertSymbol(Symbol{
			IdentityKey: "symbol:cfg:CommonModules/X/отмена", ComponentID: fxComponent,
			UID: "uid-отмена", ModuleID: f.moduleID, OriginFileID: f.fileModuleBSL,
			Kind: "procedure", NameNorm: "отмена", NameDisplay: "Отмена",
		}); err != nil {
			return err
		}
		for i := 0; i < batchRows-1; i++ {
			if _, err := tx.InsertReference(Reference{
				FileID: f.fileModuleBSL, Kind: "call", NameNorm: fmt.Sprintf("о%d", i),
				Resolution: "unresolved", Confidence: 0.5,
			}); err != nil {
				return err
			}
		}
		// Отмена приходит, когда почти полный пакет ещё в буфере: его сброс
		// делает уже runWriteTx после fn, на отменённом контексте.
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ошибка %v, ожидалась отмена контекста", err)
	}
	if got := countRows(t, s, "reference", ""); got != before {
		t.Errorf("после отката ссылок %d, было %d", got, before)
	}
	if got := countRows(t, s, "symbol", "name_norm='отмена'"); got != 0 {
		t.Error("символ отменённой транзакции выжил")
	}
	if s.writer.stmts != nil {
		t.Error("кэш подготовленных выражений пережил отмену")
	}
	if err := s.Write(context.Background(), func(tx *WriteTx) error { return tx.SetMeta("после-отмены", "1") }); err != nil {
		t.Fatalf("запись после отмены: %v", err)
	}
	assertValid(t, s, "после отмены посреди сброса пакета")
}
