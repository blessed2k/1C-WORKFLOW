package app

import (
	"context"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// ReadTx runs fn внутри ОДНОЙ read-транзакции store на весь вызов —
// правило фасада (тикет 10 п.7, архитектура §21): «иначе span и текст blob
// могут разъехаться». store.Store.Read уже даёт это через снапшот WAL;
// обёртка существует, чтобы каждый инструмент открывал транзакцию РОВНО один
// раз, единым способом, а не заново решал этот вопрос в каждом файле
// idx_*.go — и чтобы это было явно видно как примитив фасада, а не деталь
// одного инструмента.
func ReadTx[T any](ctx context.Context, st *store.Store, fn func(*store.ReadTx) (T, error)) (T, error) {
	var out T
	err := st.Read(ctx, func(tx *store.ReadTx) error {
		v, ferr := fn(tx)
		out = v
		return ferr
	})
	return out, err
}
