// find_queries_using (тикет 12) — поверх query_reference, теперь реально
// наполненной таблицы (см. internal/resolve/queries.go: DeriveQueryReference
// группирует по литералу; internal/index/publishderive.go: publishQueryReferences
// вставляет группы для static-текстов — долг тасков 08/09, закрытый этим же
// тикетом). Partial/dynamic тексты query_reference по-прежнему не несут (см.
// doc-комментарий resolve.DeriveQueryReference) — find_queries_using видит
// ровно то, что опубликовано, честно, не имитирует полноту.
package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

const (
	defaultQueryUsageLimit = 50
	maxQueryUsageLimit     = 200
)

// QueryService — сервис за find_queries_using.
type QueryService struct{ projects *Projects }

// NewQueryService строит сервис поверх общего резолвера проектов.
func NewQueryService(p *Projects) *QueryService { return &QueryService{projects: p} }

// FindQueriesUsingInput — вход find_queries_using: объект (Type+Name) ИЛИ
// поле (Field, само по себе или вместе с объектом-владельцем), плюс cursor.
// View принят для единообразия входа индексных инструментов (тикет 14,
// п.5) и валидируется, но не меняет выдачу: query_reference уже несёт
// component/layer в каждой строке через Owner (raw и без view уже
// «эффективен» в смысле §20 — список не выбирает один слой из нескольких,
// как это делает get_object, поэтому сливать здесь нечего).
type FindQueriesUsingInput struct {
	Type      string
	Name      string
	Field     string
	Component string
	View      string
	Limit     int
	Cursor    string
}

// QuerySpanRange — границы фрагмента ВНУТРИ ТЕКСТА ЗАПРОСА (не в файле —
// resolve.QueryReferenceResult.Span контракт, см. queries.go).
type QuerySpanRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// QueryOwnerRef — символ, внутри которого лежит текст запроса.
type QueryOwnerRef struct {
	UID    string      `json:"uid"`
	Module string      `json:"module"`
	Name   string      `json:"name"`
	Span   domain.Span `json:"span"`
}

// QueryUsageItem — одно использование объекта/поля в тексте запроса (R28).
type QueryUsageItem struct {
	Kind       string         `json:"kind"` // table|field|parameter|temp-table
	Name       string         `json:"name"`
	Object     string         `json:"object,omitempty"`
	Field      string         `json:"field,omitempty"`
	Owner      *QueryOwnerRef `json:"owner,omitempty"`
	QuerySpan  domain.Span    `json:"querySpan"` // текста запроса В ФАЙЛЕ
	SpanInText QuerySpanRange `json:"spanInQueryText"`
	Staticity  string         `json:"staticity"` // static|partial|dynamic
	Confidence float64        `json:"confidence"`
}

// FindQueriesUsing отвечает на find_queries_using: запросы, использующие
// объект (любое поле) или конкретное поле, найденные по query_reference —
// НЕ по подстроке в тексте (R28), staticity/confidence у каждого результата
// (R28.1).
func (s *QueryService) FindQueriesUsing(ctx context.Context, in FindQueriesUsingInput) (Response[QueryUsageItem], error) {
	op, err := s.projects.Active(ctx)
	if err != nil {
		return Response[QueryUsageItem]{}, err
	}
	mtype := strings.TrimSpace(in.Type)
	name := strings.TrimSpace(in.Name)
	field := strings.TrimSpace(in.Field)
	if mtype == "" && field == "" {
		return Response[QueryUsageItem]{}, NewError(CodeNotFound,
			"find_queries_using требует объект (type+name) или field",
			"передайте type+name объекта, field, или оба вместе").WithProject(op.Entry.ID)
	}
	if mtype != "" && name == "" || mtype == "" && name != "" {
		return Response[QueryUsageItem]{}, NewError(CodeNotFound,
			"type и name объекта передаются вместе", "укажите оба или ни одного").WithProject(op.Entry.ID)
	}
	componentID, cerr := componentFromInput(op, in.Component)
	if cerr != nil {
		return Response[QueryUsageItem]{}, cerr
	}
	if _, verr := parseView(in.View); verr != nil {
		return Response[QueryUsageItem]{}, verr.WithProject(op.Entry.ID)
	}
	limit := clampLimit(in.Limit, defaultQueryUsageLimit, maxQueryUsageLimit)
	paramsKey := fmt.Sprintf("t=%s&n=%s&f=%s&c=%s&l=%d", mtype, name, field, componentID, limit)

	type txResult struct {
		gen        domain.Generation
		items      []QueryUsageItem
		nextCursor string
	}
	res, snap, err := ReadSnapshot(ctx, op, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen
		afterKey, derr := DecodeCursor(in.Cursor, gen, paramsKey)
		if derr != nil {
			return out, derr
		}
		var afterID int64
		if afterKey != "" {
			afterID, _ = strconv.ParseInt(afterKey, 10, 64)
		}

		filter := store.QueryReferenceFilter{AfterID: afterID, Limit: limit}
		if mtype != "" {
			rows, rerr := tx.MetadataObjectsByName(mtype, domain.NormalizeName(name))
			if rerr != nil {
				return out, rerr
			}
			if len(rows) == 0 {
				return out, NotFoundError("объект", mtype+"."+name, nil).WithProject(op.Entry.ID).WithGeneration(gen)
			}
			resolved, perr := pickObjectRow(rows, componentID)
			if perr != nil {
				return out, perr.WithProject(op.Entry.ID).WithGeneration(gen)
			}
			filter.ObjectID = resolved.row.ID
		}
		if field != "" {
			filter.NameNorm = domain.NormalizeName(field)
			filter.Kind = "field" // тот же литерал, что resolve.QueryRefField сериализует в query_reference.kind
		}

		refs, rerr := tx.QueryReferences(filter)
		if rerr != nil {
			return out, rerr
		}
		hasMore := len(refs) > limit
		if hasMore {
			refs = refs[:limit]
		}

		queryCache := map[int64]store.QueryRow{}
		symbolCache := map[int64]store.SymbolRow{}
		objectCache := map[int64]store.MetadataObjectRow{}
		memberCache := map[int64]store.MetadataMemberRow{}

		for _, r := range refs {
			q, ok := queryCache[r.QueryID]
			if !ok {
				got, found, qerr := tx.QueryByID(r.QueryID)
				if qerr != nil {
					return out, qerr
				}
				if !found {
					continue // query удалён между вставкой ссылки и этим чтением — не должно случаться в рамках одной read-транзакции
				}
				q, queryCache[r.QueryID] = got, got
			}

			item := QueryUsageItem{
				Kind: r.Kind, Name: r.NameNorm, QuerySpan: q.Span,
				SpanInText: QuerySpanRange{Start: r.SpanStart, End: r.SpanEnd},
				Staticity:  q.Staticity, Confidence: q.Confidence,
			}
			if r.ObjectID != 0 {
				obj, ok := objectCache[r.ObjectID]
				if !ok {
					got, found, oerr := tx.MetadataObjectByID(r.ObjectID)
					if oerr != nil {
						return out, oerr
					}
					if found {
						obj, objectCache[r.ObjectID] = got, got
					}
				}
				item.Object = obj.NameDisplay
			}
			if r.MemberID != 0 {
				mem, ok := memberCache[r.MemberID]
				if !ok {
					got, found, merr := tx.MetadataMemberByID(r.MemberID)
					if merr != nil {
						return out, merr
					}
					if found {
						mem, memberCache[r.MemberID] = got, got
					}
				}
				item.Field = mem.NameDisplay
			}
			if q.SymbolID != 0 {
				sym, ok := symbolCache[q.SymbolID]
				if !ok {
					got, found, serr := tx.SymbolByID(q.SymbolID)
					if serr != nil {
						return out, serr
					}
					if found {
						sym, symbolCache[q.SymbolID] = got, got
					}
				}
				if sym.ID != 0 {
					item.Owner = &QueryOwnerRef{UID: sym.UID, Module: sym.ModulePath, Name: sym.NameDisplay, Span: sym.Span}
				}
			}
			out.items = append(out.items, item)
		}

		if hasMore && len(refs) > 0 {
			lastID := refs[len(refs)-1].ID
			out.nextCursor = EncodeCursor(gen, strconv.FormatInt(lastID, 10), paramsKey)
		}
		return out, nil
	})
	if err != nil {
		return Response[QueryUsageItem]{}, err
	}

	return withSnapshot(Response[QueryUsageItem]{
		Generation: res.gen, Items: res.items, TotalCount: len(res.items), NextCursor: res.nextCursor,
	}, snap), nil
}
