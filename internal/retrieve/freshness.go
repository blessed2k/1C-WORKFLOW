package retrieve

import (
	"context"
	"errors"
	"fmt"

	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// NotFreshError — require-fresh не дождался публикации в пределах deadline
// (§24 шаг 2). Оборачивает index.ErrIndexNotFresh без протечки самого типа
// index.* наружу: cmd/mcp1c различает этот случай через
// errors.As(err, &*retrieve.NotFreshError), ни разу не называя
// "internal/index" в своём исходнике (interfaces.md: «cmd/mcp1c не
// импортирует internal/index/store/resolve/parse/* напрямую — только через
// internal/app/internal/retrieve»).
type NotFreshError struct {
	Progress string
}

func (e *NotFreshError) Error() string { return fmt.Sprintf("index_not_fresh: %s", e.Progress) }

// Run — тонкая обёртка транспортного уровня вокруг Build: выполняет
// freshness-precheck (§24 шаг 2, index.Service.EnsureFresh) ДО открытия
// read-транзакции, затем одну read-транзакцию (§24 шаг 3, store.Store.Read),
// затем Build. Существует здесь, а не в cmd/mcp1c, ровно потому, что
// транспорту запрещено называть типы internal/index/internal/store у себя в
// исходнике — вызывающий передаёт *index.Service/*store.Store, полученные
// через app.Projects.Active(ctx) (op.Service/op.Store), НЕ называя их тип
// (тот же приём, что уже документирован в internal/app/projects.go у
// DefaultIndexConfig — «вызывающему не требуется называть тип, если
// результат идёт через :=»).
//
// req.ProjectID обязан быть заполнен вызывающим ДО Run (resource-ссылки
// строятся внутри Build).
func Run(ctx context.Context, svc *index.Service, st *store.Store, req Request) (Result, error) {
	mode := req.Freshness
	if mode == "" {
		mode = FreshnessAllowStale
	}
	policyMode := index.PolicyAllowStale
	if mode == FreshnessRequireFresh {
		policyMode = index.PolicyRequireFresh
	}

	fresh, err := svc.EnsureFresh(ctx, index.Policy{Mode: policyMode})
	if err != nil {
		var nf *index.ErrIndexNotFresh
		if errors.As(err, &nf) {
			return Result{}, &NotFreshError{Progress: nf.Progress}
		}
		return Result{}, fmt.Errorf("get_context_for_task: freshness: %w", err)
	}

	req.Freshness = mode
	req.Stale = !fresh.Fresh
	req.StaleReason = fresh.Reason
	req.StaleAgeSeconds = fresh.AgeSeconds

	var out Result
	err = st.Read(ctx, func(tx *store.ReadTx) error {
		r, berr := Build(ctx, tx, req)
		out = r
		return berr
	})
	return out, err
}
