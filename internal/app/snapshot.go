package app

import (
	"context"
	"fmt"

	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// Snapshot: свежесть снимка, из которого построен ответ индексного
// инструмента: признак stale и предупреждение к нему. Заполняет его только
// ReadSnapshot; сервис переносит его в ответ через withSnapshot.
type Snapshot struct {
	Stale    bool
	Warnings []Warning
}

// ReadSnapshot: единственная точка чтения индекса для инструментов:
// проверяет свежесть проекта (index.Service.CachedFreshness, ADR-036) и
// открывает РОВНО одну read-транзакцию store на вызов
// (архитектура §21: «иначе span и текст blob могут разъехаться»).
//
// Свежесть проверяется ДО транзакции, как у get_context_for_task: снимок,
// открытый после проверки, не старше проверенного состояния. Проверка
// дешёвая (исход обхода диска с TTL) и индекс не меняет: догонять его с
// диска остаётся reindex и get_context_for_task. Отказ проверки не валит
// вызов, а честно помечает ответ «свежесть не проверена» (§6: молча отдавать
// непроверенное за свежее нельзя).
func ReadSnapshot[T any](ctx context.Context, op *openProject, fn func(*store.ReadTx) (T, error)) (T, Snapshot, error) {
	snap := snapshotFreshness(ctx, op)
	out, err := readTx(ctx, op.Store, fn)
	return out, snap, err
}

func snapshotFreshness(ctx context.Context, op *openProject) Snapshot {
	fresh, err := op.Service.CachedFreshness(ctx)
	if err != nil {
		return Snapshot{Stale: true, Warnings: []Warning{{
			Code:    "freshness_check_failed",
			Message: fmt.Sprintf("свежесть индекса не проверена: %v", err),
			Hint:    "ответ построен по индексу без сверки с диском; вызовите index_status или reindex",
		}}}
	}
	if fresh.Fresh {
		return Snapshot{}
	}
	return Snapshot{Stale: true, Warnings: []Warning{staleIndexWarning(fresh.Reason, fresh.ChangedFiles, fresh.CheckAgeSeconds)}}
}

// staleIndexWarning: тот же код stale_index, что у get_context_for_task,
// текст называет причину и возраст проверки.
func staleIndexWarning(reason index.StaleReason, changed int, checkAge float64) Warning {
	var msg string
	switch reason {
	case index.ReasonFilesChanged:
		msg = fmt.Sprintf("индекс отстаёт от выгрузки: изменённых на диске файлов %d (проверка %.0f с назад)", changed, checkAge)
	case index.ReasonRebuildInProgress:
		msg = "идёт полная пересборка индекса, ответ построен по прошлому поколению"
	case index.ReasonRebuildRequired:
		msg = "индекс требует полной пересборки, ответ построен по прошлому поколению"
	default:
		msg = "индекс устарел, причина: " + string(reason)
	}
	return Warning{
		Code:    "stale_index",
		Message: msg,
		Hint:    "вызовите reindex, чтобы догнать выгрузку; get_context_for_task догоняет индекс сам",
	}
}

// withSnapshot переносит свежесть снимка в ответ: stale только добавляется
// (у get_symbol свой признак расхождения файла), предупреждение о свежести
// идёт первым.
func withSnapshot[T any](resp Response[T], snap Snapshot) Response[T] {
	resp.Stale = resp.Stale || snap.Stale
	if len(snap.Warnings) > 0 {
		resp.Warnings = append(append([]Warning(nil), snap.Warnings...), resp.Warnings...)
	}
	return resp
}

// readTx: голая read-транзакция без проверки свежести. Только для чтений,
// закреплённых за поколением или хэшем (resource-ссылки): их ответ от
// свежести диска не зависит. Инструменты читают через ReadSnapshot.
func readTx[T any](ctx context.Context, st *store.Store, fn func(*store.ReadTx) (T, error)) (T, error) {
	var out T
	err := st.Read(ctx, func(tx *store.ReadTx) error {
		v, ferr := fn(tx)
		out = v
		return ferr
	})
	return out, err
}
