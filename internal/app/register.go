// find_register_writes поверх register_access.
package app

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

const (
	defaultRegisterAccessLimit = 50
	maxRegisterAccessLimit     = 200
)

// RegisterService — сервис за find_register_writes.
type RegisterService struct{ projects *Projects }

// NewRegisterService строит сервис поверх общего резолвера проектов.
func NewRegisterService(p *Projects) *RegisterService { return &RegisterService{projects: p} }

// FindRegisterWritesInput: вход find_register_writes (register,
// modes?, component?, symbol?, minConfidence?, view, cursor). Symbol: uid
// символа-владельца доступа (тот же формат, что у find_references — сам uid,
// не module+name: доступ ищется по конкретному владельцу, не по подстроке).
// View валидируется, но не меняет выдачу: register_access это
// список, не get_object-подобный «выбор одной строки», каждая строка уже
// несёт свой Component — сливать нечего (см. FindQueriesUsingInput.View,
// та же причина).
type FindRegisterWritesInput struct {
	Register      string
	Modes         string // csv: write,read,movement,clear; пусто -> write (умолчание из брифа)
	Component     string
	Symbol        string
	MinConfidence float64
	View          string
	Limit         int
	Cursor        string
}

// RegisterAccessItem: один доступ к регистру.
type RegisterAccessItem struct {
	Register      string             `json:"register"`
	Mode          string             `json:"mode"`
	Component     domain.ComponentID `json:"component"`
	File          string             `json:"file"`
	Owner         *QueryOwnerRef     `json:"owner,omitempty"`
	InTransaction *bool              `json:"inTransaction,omitempty"`
	Static        bool               `json:"static"`
	Confidence    float64            `json:"confidence"`
	Span          domain.Span        `json:"span"`
}

// WarnRegisterWritesDeclaredOnly: у регистра нет кодовых записей, но
// документы объявляют в него движения (RegisterRecords): их проводит механизм,
// и пустой список иначе читался бы как «в регистр никто не пишет».
const WarnRegisterWritesDeclaredOnly = "register_writes_declared_only"

// declaredEdgesPage: размер страницы при чтении рёбер writes-declared.
const declaredEdgesPage = 500

var validRegisterModes = map[string]bool{"write": true, "read": true, "movement": true, "clear": true}

// parseRegisterModes разбирает csv modes; пусто -> {write} (умолчание брифа:
// «modes? (write|read|movement|clear, default write)»).
func parseRegisterModes(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{"write"}, nil
	}
	var out []string
	for _, m := range strings.Split(raw, ",") {
		m = strings.ToLower(strings.TrimSpace(m))
		if m == "" {
			continue
		}
		if !validRegisterModes[m] {
			return nil, fmt.Errorf("режим %q неизвестен, допустимы write, read, movement, clear", m)
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return []string{"write"}, nil
	}
	return out, nil
}

// FindRegisterWrites отвечает на find_register_writes: кто пишет в регистр и
// (по modes) кто из него читает — режим, символ-владелец, span,
// транзакционность, static/dynamic и confidence.
func (s *RegisterService) FindRegisterWrites(ctx context.Context, in FindRegisterWritesInput) (Response[RegisterAccessItem], error) {
	op, err := s.projects.Active(ctx)
	if err != nil {
		return Response[RegisterAccessItem]{}, err
	}
	register := strings.TrimSpace(in.Register)
	if register == "" {
		return Response[RegisterAccessItem]{}, NewError(CodeNotFound, "find_register_writes требует непустой register",
			"передайте имя регистра, например ОстаткиТоваров").WithProject(op.Entry.ID)
	}
	modes, merr := parseRegisterModes(in.Modes)
	if merr != nil {
		return Response[RegisterAccessItem]{}, NewError(CodeNotFound, merr.Error(),
			"допустимые значения modes: write, read, movement, clear (через запятую)").WithProject(op.Entry.ID)
	}
	componentID, cerr := componentFromInput(op, in.Component)
	if cerr != nil {
		return Response[RegisterAccessItem]{}, cerr
	}
	if _, verr := parseView(in.View); verr != nil {
		return Response[RegisterAccessItem]{}, verr.WithProject(op.Entry.ID)
	}
	limit := clampLimit(in.Limit, defaultRegisterAccessLimit, maxRegisterAccessLimit)
	nameNorm := domain.NormalizeName(register)
	paramsKey := fmt.Sprintf("r=%s&m=%s&c=%s&s=%s&mc=%g&l=%d",
		nameNorm, strings.Join(modes, ","), componentID, in.Symbol, in.MinConfidence, limit)

	type txResult struct {
		gen        domain.Generation
		items      []RegisterAccessItem
		nextCursor string
		warnings   []Warning
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

		var symbolID int64
		if strings.TrimSpace(in.Symbol) != "" {
			nid, ok, nerr := tx.NodeID(strings.TrimSpace(in.Symbol))
			if nerr != nil {
				return out, nerr
			}
			if !ok {
				return out, NotFoundError("символ", in.Symbol, nil).WithProject(op.Entry.ID).WithGeneration(gen)
			}
			symbolID = nid
		}

		rows, rerr := tx.RegisterAccesses(store.RegisterAccessFilter{
			RegisterNameNorm: nameNorm, Modes: modes, ComponentID: componentID, SymbolID: symbolID,
			MinConfidence: in.MinConfidence, AfterID: afterID, Limit: limit,
		})
		if rerr != nil {
			return out, rerr
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}

		symbolCache := map[int64]store.SymbolRow{}
		for _, r := range rows {
			file, ok, ferr := tx.SourceFileByID(r.FileID)
			if ferr != nil {
				return out, ferr
			}
			item := RegisterAccessItem{
				Register: register, Mode: r.Mode, Component: domain.ComponentID(r.ComponentID),
				InTransaction: r.InTransaction, Static: r.Static, Confidence: r.Confidence, Span: r.Span,
			}
			if ok {
				item.File = file.RelPath
			}
			if r.SymbolID != 0 {
				sym, cached := symbolCache[r.SymbolID]
				if !cached {
					got, found, serr := tx.SymbolByID(r.SymbolID)
					if serr != nil {
						return out, serr
					}
					if found {
						sym, symbolCache[r.SymbolID] = got, got
					}
				}
				if sym.ID != 0 {
					item.Owner = &QueryOwnerRef{UID: sym.UID, Module: sym.ModulePath, Name: sym.NameDisplay, Span: sym.Span}
				}
			}
			out.items = append(out.items, item)
		}

		if hasMore && len(rows) > 0 {
			out.nextCursor = EncodeCursor(gen, strconv.FormatInt(rows[len(rows)-1].ID, 10), paramsKey)
		}
		if len(out.items) == 0 && afterID == 0 && symbolID == 0 && slices.Contains(modes, "write") {
			w, werr := declaredMovementsWarning(tx, register, nameNorm, componentID, modes)
			if werr != nil {
				return out, werr
			}
			if w != nil {
				out.warnings = append(out.warnings, *w)
			}
		}
		return out, nil
	})
	if err != nil {
		return Response[RegisterAccessItem]{}, err
	}

	items := res.items
	if items == nil {
		// Пустой ответ это пустой список, не null: вызывающий не должен
		// отличать «ничего не нашлось» от «поле потерялось».
		items = []RegisterAccessItem{}
	}
	return withSnapshot(Response[RegisterAccessItem]{
		Generation: res.gen, Warnings: res.warnings, Items: items, TotalCount: len(items), NextCursor: res.nextCursor,
	}, snap), nil
}

// declaredMovementsWarning считает объекты, объявившие движения в регистр
// (рёбра writes-declared объектного графа), и возвращает подсказку, если они
// есть. Регистр ищется по имени среди видов-регистров, с учётом component.
// Если одно имя носят регистры нескольких видов, подсказка называет все виды.
func declaredMovementsWarning(tx *store.ReadTx, register, nameNorm, componentID string, modes []string) (*Warning, error) {
	objects, err := tx.MetadataObjectsByNameNormAnyType(nameNorm)
	if err != nil {
		return nil, err
	}
	writers := map[int64]bool{}
	var mtypes []string
	for _, obj := range objects {
		kind, ok := domain.MetaKindByMType(obj.MType)
		if !ok || !kind.IsRegister {
			continue
		}
		if componentID != "" && obj.ComponentID != componentID {
			continue
		}
		before := len(writers)
		// упрощение: рёбра читаются страницами целиком; у регистра их порядка
		// числа документов-регистраторов (сотни), счётчик в SQL не нужен.
		var after int64
		for {
			rows, rerr := tx.ObjectDataEdges(store.ObjectEdgeFilter{
				ObjectID: obj.ID, Direction: store.EdgeDirectionIn,
				Kinds: []string{store.EdgeWritesDeclared}, AfterID: after, Limit: declaredEdgesPage,
			})
			if rerr != nil {
				return nil, rerr
			}
			page := rows[:min(len(rows), declaredEdgesPage)]
			for _, r := range page {
				writers[r.FromObjectID] = true
				after = r.ID
			}
			if len(rows) <= declaredEdgesPage {
				break
			}
		}
		if len(writers) > before && !slices.Contains(mtypes, obj.MType) {
			mtypes = append(mtypes, obj.MType)
		}
	}
	if len(writers) == 0 {
		return nil, nil
	}
	objectType := mtypes[0]
	if len(mtypes) > 1 {
		slices.Sort(mtypes)
		objectType = "<" + strings.Join(mtypes, "|") + ">"
	}
	return &Warning{
		Code: WarnRegisterWritesDeclaredOnly,
		Message: fmt.Sprintf("кодовых записей нет (modes=%s), документы проводятся механизмом; объявленные движения: %d %s",
			strings.Join(modes, ","), len(writers), objectsWord(len(writers))),
		Hint: fmt.Sprintf("см. object_graph objectType=%s objectName=%s direction=in", objectType, register),
	}, nil
}

// objectsWord склоняет «объект» по числу.
func objectsWord(n int) string {
	switch {
	case n%100 >= 11 && n%100 <= 14:
		return "объектов"
	case n%10 == 1:
		return "объект"
	case n%10 >= 2 && n%10 <= 4:
		return "объекта"
	}
	return "объектов"
}
