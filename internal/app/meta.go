// Метаданные и формы (тикет 12): get_object, get_form_handlers.
// find_queries_using и find_register_writes живут рядом, в query.go и
// register.go — все три файла делят resolveObjectRow/objectParts/componentFromInput.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// MetadataService — сервис за get_object и get_form_handlers (interfaces.md:
// по одному сервису на группу связанных инструментов).
type MetadataService struct{ projects *Projects }

// NewMetadataService строит сервис поверх общего резолвера проектов.
func NewMetadataService(p *Projects) *MetadataService { return &MetadataService{projects: p} }

// --- вход ---

// GetObjectInput — вход get_object (тикет 12: «type + name, parts?, view»).
// Component — НЕобязательное сужение: без него и без view=effective, когда
// один и тот же объект заимствован в несколько компонентов (базовая
// конфигурация + расширение), выбирается строка базового слоя (см.
// pickObjectRow). View=effective без component сливает Members/Forms всех
// слоёв (тикет 14, см. GetObject).
type GetObjectInput struct {
	Type      string
	Name      string
	Component string
	Parts     string
	View      string
}

// GetFormHandlersInput — вход get_form_handlers (owner=type+name, form?).
// View принимается и валидируется (тикет 14, п.5: «view добавляется во все
// индексные инструменты»), но не меняет выдачу — обработчики форм расширений
// пока не сливаются с базовыми (docs/tools-index.md, раздел «Формы
// расширений»); открытый пункт для отдельного тикета, не эта функция.
type GetFormHandlersInput struct {
	OwnerType string
	OwnerName string
	Form      string
	Component string
	View      string
}

// --- выход: get_object ---

// ObjectMemberItem — реквизит/ресурс/измерение/табличная часть или её
// реквизит (R27: «реквизиты, ТЧ, измерения/ресурсы»).
type ObjectMemberItem struct {
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	Types   []string `json:"types,omitempty"`
	Indexed bool     `json:"indexed,omitempty"`
	// Parent — отображаемое имя родительской табличной части, пусто у
	// членов верхнего уровня объекта.
	Parent string `json:"parent,omitempty"`
	// Layer — слой, добавивший этот член (ADR-4: «каждый effective-факт
	// несёт provenance»). Заполнен всегда, не только при view=effective:
	// у объекта в одном слое совпадает с ObjectItem.Layer.
	Layer string `json:"layer,omitempty"`
}

// ObjectFormItem — форма объекта со списком её команд (R27.1).
type ObjectFormItem struct {
	Name     string   `json:"name"`
	Commands []string `json:"commands,omitempty"`
	// Layer — слой, добавивший эту форму (см. ObjectMemberItem.Layer).
	Layer string `json:"layer,omitempty"`
}

// ObjectSubscriptionItem — подписка, чей источник — этот объект (R30, R30.1).
type ObjectSubscriptionItem struct {
	Name       string `json:"name"`
	SourceKind string `json:"sourceKind"`
	Event      string `json:"event"`
	Handler    string `json:"handler"`
	Resolution string `json:"resolution"`
}

// ObjectScheduledJobItem — регламентное задание. Заполняется, только когда
// запрошенный объект сам — ScheduledJob (см. doc-комментарий resolveScheduledJobs):
// схема раздела 15 не даёт способа связать произвольный объект с заданием,
// которое его использует.
type ObjectScheduledJobItem struct {
	Name       string `json:"name"`
	Method     string `json:"method"`
	Use        bool   `json:"use"`
	Predefined bool   `json:"predefined"`
	Resolution string `json:"resolution"`
}

// ObjectRoleRightItem — одно право одной роли на объект (R30, R30.2: сырой
// факт, включая value=false; ИЛИ-агрегация — дело читающего, см.
// resolve.EffectiveRoleObjectRights).
type ObjectRoleRightItem struct {
	Role             string `json:"role"`
	Right            string `json:"right"`
	Value            bool   `json:"value"`
	RLS              string `json:"rls,omitempty"`
	SetForNewObjects bool   `json:"setForNewObjects"`
}

// ObjectItem — items[0] ответа get_object.
type ObjectItem struct {
	Type      string             `json:"type"`
	Name      string             `json:"name"`
	UUID      string             `json:"uuid,omitempty"`
	Synonym   string             `json:"synonym,omitempty"`
	Component domain.ComponentID `json:"component"`
	Layer     string             `json:"layer"`

	Members       []ObjectMemberItem       `json:"members,omitempty"`
	Forms         []ObjectFormItem         `json:"forms,omitempty"`
	Subscriptions []ObjectSubscriptionItem `json:"subscriptions,omitempty"`
	ScheduledJobs []ObjectScheduledJobItem `json:"scheduledJobs,omitempty"`
	RoleRights    []ObjectRoleRightItem    `json:"roleRights,omitempty"`
}

// objectParts — части, реально запрошенные get_object; nil/пусто -> все.
// Тот же csv-вокабуляр, что objectStructureInput.Parts у старого
// get_object_structure (forms/attributes/...), но со своим набором ключей —
// у get_object свой набор блоков (подписки, задания, права), которых у
// старого инструмента нет.
type objectParts struct {
	members, forms, subscriptions, jobs, roles bool
}

func parseObjectParts(raw string) objectParts {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return objectParts{members: true, forms: true, subscriptions: true, jobs: true, roles: true}
	}
	var p objectParts
	for _, part := range strings.Split(raw, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "members", "attributes", "tabular":
			p.members = true
		case "forms", "commands":
			p.forms = true
		case "subscriptions":
			p.subscriptions = true
		case "jobs", "scheduledjobs":
			p.jobs = true
		case "roles":
			p.roles = true
		case "other":
			p.subscriptions, p.jobs, p.roles = true, true, true
		}
	}
	return p
}

// resolvedObject — строка metadata_object, выбранная pickObjectRow, плюс
// предупреждения, которые стоило вернуть вызывающему инструменту (найдено
// несколько слоёв, а выбор — единственный, т.е. view=raw).
type resolvedObject struct {
	row      store.MetadataObjectRow
	warnings []Warning
}

// pickObjectRow выбирает ОДНУ строку metadata_object из совпадений по
// mtype+name_norm — раздел 20, слой view=raw (или view=effective с явным
// component, см. GetObject: тогда пользователь сам сузил выбор до одного
// слоя, сливать нечего). Слияние слоёв для view=effective без component
// делает mergeObjectRows (effective.go), не эта функция. Component сужает
// выбор явно; без него предпочитается base-слой, иначе — первая по
// component_id строка, с warning о прочих найденных слоях.
func pickObjectRow(rows []store.MetadataObjectRow, componentFilter string) (resolvedObject, *Error) {
	if componentFilter != "" {
		for _, r := range rows {
			if r.ComponentID == componentFilter {
				return resolvedObject{row: r}, nil
			}
		}
		return resolvedObject{}, NewError(CodeNotFound,
			fmt.Sprintf("объект %s.%s не найден в компоненте %q", rows[0].MType, rows[0].NameDisplay, componentFilter),
			"проверьте component или вызовите без него — тогда выбирается слой по умолчанию")
	}

	var warnings []Warning
	chosen := rows[0]
	for _, r := range rows {
		if r.Layer == "base" {
			chosen = r
			break
		}
	}
	if len(rows) > 1 {
		var others []string
		for _, r := range rows {
			if r.ComponentID != chosen.ComponentID {
				others = append(others, r.ComponentID)
			}
		}
		if len(others) > 0 {
			warnings = append(warnings, Warning{
				Code:    "object_in_multiple_layers",
				Message: fmt.Sprintf("объект найден также в компонентах: %s (view=raw показывает только %s)", strings.Join(others, ", "), chosen.ComponentID),
				Hint:    "передайте component, чтобы выбрать конкретный слой явно",
			})
		}
	}
	return resolvedObject{row: chosen, warnings: warnings}, nil
}

// GetObject отвечает на get_object: структура объекта из индекса, а не из
// XML (R27), с формами/командами (R27.1), подписками/заданиями/правами
// (R30). parts сужает набор блоков и обращений к store.
func (s *MetadataService) GetObject(ctx context.Context, in GetObjectInput) (Response[ObjectItem], error) {
	op, err := s.projects.Active(ctx)
	if err != nil {
		return Response[ObjectItem]{}, err
	}
	mtype := strings.TrimSpace(in.Type)
	name := strings.TrimSpace(in.Name)
	if mtype == "" || name == "" {
		return Response[ObjectItem]{}, NewError(CodeNotFound, "get_object требует непустые type и name",
			"передайте type (например Catalog) и name (например Товары)").WithProject(op.Entry.ID)
	}
	componentID, cerr := componentFromInput(op, in.Component)
	if cerr != nil {
		return Response[ObjectItem]{}, cerr
	}
	view, verr := parseView(in.View)
	if verr != nil {
		return Response[ObjectItem]{}, verr.WithProject(op.Entry.ID)
	}
	nameNorm := domain.NormalizeName(name)
	parts := parseObjectParts(in.Parts)

	type txResult struct {
		gen  domain.Generation
		item ObjectItem
		warn []Warning
	}
	res, snap, err := ReadSnapshot(ctx, op, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen

		rows, rerr := tx.MetadataObjectsByName(mtype, nameNorm)
		if rerr != nil {
			return out, rerr
		}
		if len(rows) == 0 {
			return out, NotFoundError("объект", mtype+"."+name, nil).WithProject(op.Entry.ID).WithGeneration(gen)
		}

		// effective без явного component: сливаем ВСЕ слои (базовый +
		// расширения) — раздел 20. С явным component пользователь уже
		// сузил выбор до одного слоя, сливать нечего (та же логика, что
		// view=raw). См. sortObjectRowsByLayer (effective.go).
		var layerRows []store.MetadataObjectRow
		if view == domain.ViewEffective && componentID == "" {
			layerRows = sortObjectRowsByLayer(op.Manifest, rows)
		} else {
			resolved, perr := pickObjectRow(rows, componentID)
			if perr != nil {
				return out, perr.WithProject(op.Entry.ID).WithGeneration(gen)
			}
			out.warn = resolved.warnings
			layerRows = []store.MetadataObjectRow{resolved.row}
		}
		row := layerRows[0] // primary: base-слой (или единственный выбранный) — источник Type/Name/UUID/Synonym

		item := ObjectItem{
			Type: row.MType, Name: row.NameDisplay, UUID: row.UUID, Synonym: row.Synonym,
			Component: domain.ComponentID(row.ComponentID), Layer: row.Layer,
		}

		if parts.members {
			for _, lr := range layerRows {
				members, merr := tx.MetadataMembers(lr.ID)
				if merr != nil {
					return out, merr
				}
				byID := make(map[int64]string, len(members))
				for _, m := range members {
					byID[m.ID] = m.NameDisplay
				}
				for _, m := range members {
					item.Members = append(item.Members, ObjectMemberItem{
						Kind: m.Kind, Name: m.NameDisplay, Types: splitTypesJSON(m.TypesJSON),
						Indexed: m.Indexed, Parent: byID[m.ParentMember], Layer: lr.Layer,
					})
				}
			}
		}

		if parts.forms {
			for _, lr := range layerRows {
				forms, ferr := tx.FormsByOwner(lr.ID)
				if ferr != nil {
					return out, ferr
				}
				for _, f := range forms {
					cmds, cerr := tx.FormCommands(f.ID)
					if cerr != nil {
						return out, cerr
					}
					fi := ObjectFormItem{Name: f.NameDisplay, Layer: lr.Layer}
					for _, c := range cmds {
						fi.Commands = append(fi.Commands, c.NameDisplay)
					}
					item.Forms = append(item.Forms, fi)
				}
			}
		}

		if parts.subscriptions {
			subs, serr := tx.EventSubscriptionsBySourceNames(subscriptionSourceCandidates(row.MType, row.NameDisplay))
			if serr != nil {
				return out, serr
			}
			for _, sub := range subs {
				item.Subscriptions = append(item.Subscriptions, ObjectSubscriptionItem{
					Name: sub.NameDisplay, SourceKind: sub.SourceKind, Event: sub.Event,
					Handler: sub.HandlerNameNorm, Resolution: sub.Resolution,
				})
			}
		}

		if parts.jobs {
			jobs, jerr := tx.ScheduledJobsByName(row.ComponentID, row.NameNorm)
			if jerr != nil {
				return out, jerr
			}
			for _, j := range jobs {
				item.ScheduledJobs = append(item.ScheduledJobs, ObjectScheduledJobItem{
					Name: j.NameDisplay, Method: j.MethodNameNorm, Use: j.Use,
					Predefined: j.Predefined, Resolution: j.Resolution,
				})
			}
		}

		if parts.roles {
			rights, rrerr := tx.RoleRightsByObjectID(row.ID)
			if rrerr != nil {
				return out, rrerr
			}
			for _, rr := range rights {
				item.RoleRights = append(item.RoleRights, ObjectRoleRightItem{
					Role: rr.RoleNameDisplay, Right: rr.RightName, Value: rr.Value,
					RLS: rr.RLS, SetForNewObjects: rr.SetForNewObjects,
				})
			}
		}

		out.item = item
		return out, nil
	})
	if err != nil {
		return Response[ObjectItem]{}, err
	}

	return withSnapshot(Response[ObjectItem]{
		Generation: res.gen, Items: []ObjectItem{res.item}, TotalCount: 1, Warnings: res.warn,
	}, snap), nil
}

// subscriptionSourceCandidates строит нормализованные варианты
// source_name_norm, под которыми подписка на этот объект могла быть
// записана платформой (см. doc-комментарий store.EventSubscriptionsBySourceNames):
// конкретный тип ("<mtype>object.<имя>") и голый вид, покрывающий ВСЕ объекты
// этого MType ("<mtype>object"). Ref/Manager-формы источника осознанно не
// перечислены — упрощение, см. handoff.
func subscriptionSourceCandidates(mtype, nameDisplay string) []string {
	return []string{
		domain.NormalizeName(mtype + "Object." + nameDisplay),
		domain.NormalizeName(mtype + "Object"),
	}
}

// splitTypesJSON разбирает JSON-массив типов члена метаданных (store.
// MetadataMemberRow.TypesJSON, тот же формат, что publishMetadataObject
// пишет через json.Marshal(m.Types)). Пусто/невалидно -> nil, не паника.
func splitTypesJSON(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// --- выход: get_form_handlers ---

// HandlerSymbolRef — символ-обработчик, на который разрешилась привязка
// (R43: «binding + resolution в символ со span»). nil в FormHandlerItem,
// когда Resolution != resolved.
type HandlerSymbolRef struct {
	UID    string      `json:"uid"`
	Module string      `json:"module"`
	Name   string      `json:"name"`
	Span   domain.Span `json:"span"`
}

// FormHandlerItem — одна привязка обработчика к событию формы (R43).
// Diagnostics непусто РОВНО когда Resolution=unresolved (R43.1: «обработчик
// объявлен, но не найден в модуле — resolution=unresolved + diagnostic, а не
// пустая выдача») — store.handler_binding не несёт отдельной строки
// diagnostic для этого случая (index/publishderive.go её не вставляет),
// поэтому diagnostic синтезируется здесь, на чтении, из самого факта
// unresolved-привязки — не имитация, тот же код что дал бы пайплайн.
type FormHandlerItem struct {
	Form        string              `json:"form"`
	Source      string              `json:"source"`
	Event       string              `json:"event"`
	Handler     string              `json:"handler"`
	Resolution  domain.Resolution   `json:"resolution"`
	Symbol      *HandlerSymbolRef   `json:"symbol,omitempty"`
	Diagnostics []domain.Diagnostic `json:"diagnostics,omitempty"`
}

// GetFormHandlers отвечает на get_form_handlers: обработчики формы
// (или всех форм объекта, если form не задан), каждый — с resolution в
// символ (R43, R43.1).
func (s *MetadataService) GetFormHandlers(ctx context.Context, in GetFormHandlersInput) (Response[FormHandlerItem], error) {
	op, err := s.projects.Active(ctx)
	if err != nil {
		return Response[FormHandlerItem]{}, err
	}
	mtype := strings.TrimSpace(in.OwnerType)
	name := strings.TrimSpace(in.OwnerName)
	if mtype == "" || name == "" {
		return Response[FormHandlerItem]{}, NewError(CodeNotFound, "get_form_handlers требует непустые owner type и name",
			"передайте type владельца формы (например Catalog) и name (например Товары)").WithProject(op.Entry.ID)
	}
	componentID, cerr := componentFromInput(op, in.Component)
	if cerr != nil {
		return Response[FormHandlerItem]{}, cerr
	}
	if _, verr := parseView(in.View); verr != nil {
		return Response[FormHandlerItem]{}, verr.WithProject(op.Entry.ID)
	}
	nameNorm := domain.NormalizeName(name)
	formNameNorm := domain.NormalizeName(strings.TrimSpace(in.Form))

	type txResult struct {
		gen   domain.Generation
		items []FormHandlerItem
	}
	res, snap, err := ReadSnapshot(ctx, op, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen

		rows, rerr := tx.MetadataObjectsByName(mtype, nameNorm)
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
		objID := resolved.row.ID

		var forms []store.FormRow
		if formNameNorm != "" {
			f, ok, ferr := tx.FormByOwnerAndName(objID, formNameNorm)
			if ferr != nil {
				return out, ferr
			}
			if !ok {
				return out, NotFoundError("форма", in.Form, nil).WithProject(op.Entry.ID).WithGeneration(gen)
			}
			forms = []store.FormRow{f}
		} else {
			fs, ferr := tx.FormsByOwner(objID)
			if ferr != nil {
				return out, ferr
			}
			forms = fs
		}

		for _, f := range forms {
			bindings, berr := tx.HandlerBindingsByForm(f.ID)
			if berr != nil {
				return out, berr
			}
			for _, b := range bindings {
				item := FormHandlerItem{
					Form: f.NameDisplay, Source: b.Source, Event: b.Event,
					Handler: b.HandlerNameNorm, Resolution: domain.Resolution(b.Resolution),
				}
				if b.Resolution == string(domain.ResolutionResolved) && b.HandlerSymbolID != 0 {
					sym, ok, serr := tx.SymbolByID(b.HandlerSymbolID)
					if serr != nil {
						return out, serr
					}
					if ok {
						item.Symbol = &HandlerSymbolRef{
							UID: sym.UID, Module: sym.ModulePath, Name: sym.NameDisplay, Span: sym.Span,
						}
					}
				}
				if b.Resolution == string(domain.ResolutionUnresolved) {
					item.Diagnostics = []domain.Diagnostic{{
						Code: "handler_unresolved", Severity: domain.SeverityWarning,
						Message: fmt.Sprintf("обработчик %q события %s.%s объявлен, но не найден в модуле формы %q",
							b.HandlerNameNorm, b.Source, b.Event, f.NameDisplay),
						Component: domain.ComponentID(resolved.row.ComponentID),
					}}
				}
				out.items = append(out.items, item)
			}
		}
		return out, nil
	})
	if err != nil {
		return Response[FormHandlerItem]{}, err
	}

	return withSnapshot(Response[FormHandlerItem]{Generation: res.gen, Items: res.items, TotalCount: len(res.items)}, snap), nil
}
