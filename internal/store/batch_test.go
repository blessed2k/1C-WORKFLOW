package store

import (
	"context"
	"errors"
	"fmt"
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
		before := tx.c.stmts.prepared
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
		if d := tx.c.stmts.prepared - before; d > 1 {
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
