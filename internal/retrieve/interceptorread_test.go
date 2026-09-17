package retrieve

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// failingSymbolFinder — источник символов, у которого падает РОВНО выборка
// символа перехватчика: все прочие чтения идут обычной транзакцией, поэтому
// сборка доходит до места отказа целой, а не разваливается раньше.
// Отказ узнаётся по компоненту-расширению: базовый слой ищется тем же
// FindSymbols (findPostingHandler), и глушить его нельзя — иначе обработчика
// не будет вовсе и ветка снова окажется недостижимой.
type failingSymbolFinder struct {
	tx        *store.ReadTx
	failComp  string
	failCount int
}

var errSymbolReadFailed = errors.New("диск не читается")

func (f *failingSymbolFinder) FindSymbols(q store.SymbolSearch) ([]store.SymbolRow, error) {
	if q.ComponentID == f.failComp {
		f.failCount++
		return nil, errSymbolReadFailed
	}
	return f.tx.FindSymbols(q)
}

// TestInterceptorSymbolReadFailedWarns — находка ревью по таску 09: из
// четырёх исходов поиска символа перехватчика единственный БЕЗ теста — отказ
// чтения. Ветка заведена ради того, чтобы сбой не выглядел пустотой, и
// проверяется тем же способом, что остальные три: отказ гонится через шов
// Build до предупреждений ответа.
//
// Мутация, которая обязана красить тест: вернуть в interceptorSymbol
// молчаливый `return ..., nil, false` на ошибке — перехватчик в ответе
// останется, движений у него не будет, и ни одного слова о том, почему.
func TestInterceptorSymbolReadFailedWarns(t *testing.T) {
	st := openFixtureStore(t)
	seedInterceptorSymbolFixture(t, st, 1)

	var res Result
	finder := &failingSymbolFinder{failComp: "ext-a"}
	err := st.Read(context.Background(), func(tx *store.ReadTx) error {
		finder.tx = tx
		r, berr := buildWithSymbols(context.Background(), tx, finder, Request{
			Task: postingTask, ProjectID: "p", View: "effective",
		})
		res = r
		return berr
	})
	if err != nil {
		t.Fatalf("buildWithSymbols: %v", err)
	}
	if finder.failCount == 0 {
		t.Fatalf("отказ чтения ни разу не сработал — тест не доходит до проверяемой ветки")
	}

	codes := warningCodes(res)
	if codes["interceptor_symbol_read_failed"] != 1 {
		t.Fatalf("interceptor_symbol_read_failed = %d, want 1: %+v", codes["interceptor_symbol_read_failed"], res.Warnings)
	}
	var msg string
	for _, w := range res.Warnings {
		if w.Code == "interceptor_symbol_read_failed" {
			msg = w.Message
		}
	}
	if !strings.Contains(msg, errSymbolReadFailed.Error()) {
		t.Errorf("предупреждение не несёт причину отказа %q: %q", errSymbolReadFailed, msg)
	}
	// Сам перехватчик в ответе остаётся — иначе предупреждение объясняло бы
	// пустоту, которой не видно.
	var hasIntercept bool
	for _, s := range res.Signatures {
		if s.Kind == "Вместо" {
			hasIntercept = true
		}
	}
	if !hasIntercept {
		t.Errorf("факт перехвата пропал из ответа: %+v", res.Signatures)
	}
}
