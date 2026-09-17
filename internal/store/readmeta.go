package store

import (
	"database/sql"
	"errors"
)

// Файл — типизированные ВЫБОРКИ (interfaces.md: «store... выставляет...
// типизированные выборки/вставки — методы на *ReadTx/*WriteTx»), нужные
// таску 12 (get_object, get_form_handlers): metadata_object/metadata_member,
// form/form_element/form_command, event_subscription, scheduled_job,
// role/role_right, handler_binding, symbol-по-id, source_file-по-id. Ровно
// столько SQL, сколько требуют эти два инструмента — не общий ORM-слой.
//
// SQL живёт ТОЛЬКО здесь (RuleSQLOnlyInStore, internal/arch): app сервисы
// таска 12 читают исключительно через эти методы, ни одного SELECT вне
// internal/store.

// MetadataObjectRow — одна строка metadata_object.
type MetadataObjectRow struct {
	ID          int64
	ComponentID string
	UUID        string
	MType       string
	NameNorm    string
	NameDisplay string
	Synonym     string
	FileID      int64
	PropsJSON   string
	Layer       string
}

// MetadataObjectsByName ищет объект метаданных по виду и нормализованному
// имени БЕЗ фильтра по компоненту: store — одна БД одного logical project, и
// один и тот же объект (borrowed) может иметь строку в нескольких компонентах
// (базовая конфигурация + расширение) — раздел 20, view=raw|effective решает
// вызывающий (internal/app), эта функция отдаёт ВСЕ совпадения.
func (tx *ReadTx) MetadataObjectsByName(mtype, nameNorm string) ([]MetadataObjectRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT id,component_id,uuid,mtype,name_norm,name_display,synonym,file_id,props,layer
		FROM metadata_object WHERE mtype=? AND name_norm=? ORDER BY component_id`, mtype, nameNorm)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetadataObjectRow
	for rows.Next() {
		var r MetadataObjectRow
		var uuid, synonym, props sql.NullString
		if err := rows.Scan(&r.ID, &r.ComponentID, &uuid, &r.MType, &r.NameNorm, &r.NameDisplay,
			&synonym, &r.FileID, &props, &r.Layer); err != nil {
			return nil, err
		}
		r.UUID, r.Synonym, r.PropsJSON = uuid.String, synonym.String, props.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// MetadataObjectByID читает один объект метаданных по его node.id — нужен,
// чтобы показать владельца поля/таблицы в find_queries_using и
// find_register_writes по object_id ссылки, не разбирая identity_key заново.
func (tx *ReadTx) MetadataObjectByID(id int64) (MetadataObjectRow, bool, error) {
	if err := tx.check(); err != nil {
		return MetadataObjectRow{}, false, err
	}
	var r MetadataObjectRow
	var uuid, synonym, props sql.NullString
	err := tx.c.sc.QueryRowContext(tx.ctx, `SELECT id,component_id,uuid,mtype,name_norm,name_display,synonym,file_id,props,layer
		FROM metadata_object WHERE id=?`, id).
		Scan(&r.ID, &r.ComponentID, &uuid, &r.MType, &r.NameNorm, &r.NameDisplay, &synonym, &r.FileID, &props, &r.Layer)
	if errors.Is(err, sql.ErrNoRows) {
		return MetadataObjectRow{}, false, nil
	}
	if err != nil {
		return MetadataObjectRow{}, false, err
	}
	r.UUID, r.Synonym, r.PropsJSON = uuid.String, synonym.String, props.String
	return r, true, nil
}

// MetadataMemberRow — одна строка metadata_member (реквизит/ресурс/измерение/
// табличная часть или реквизит табличной части).
type MetadataMemberRow struct {
	ID           int64
	ObjectID     int64
	OriginFileID int64
	Kind         string
	NameNorm     string
	NameDisplay  string
	TypesJSON    string
	Indexed      bool
	ParentMember int64
}

// MetadataMembers отдаёт все члены объекта, плоским списком (ParentMember
// строит иерархию табличных частей на стороне вызывающего — тем же приёмом,
// что и остальные aspect-таблицы раздела 15).
func (tx *ReadTx) MetadataMembers(objectID int64) ([]MetadataMemberRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT id,object_id,origin_file_id,kind,name_norm,name_display,types,indexed,parent_member
		FROM metadata_member WHERE object_id=? ORDER BY id`, objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetadataMemberRow
	for rows.Next() {
		var r MetadataMemberRow
		var types sql.NullString
		var indexed sql.NullInt64
		var parent sql.NullInt64
		if err := rows.Scan(&r.ID, &r.ObjectID, &r.OriginFileID, &r.Kind, &r.NameNorm, &r.NameDisplay,
			&types, &indexed, &parent); err != nil {
			return nil, err
		}
		r.TypesJSON, r.Indexed, r.ParentMember = types.String, indexed.Int64 != 0, parent.Int64
		out = append(out, r)
	}
	return out, rows.Err()
}

// MetadataMemberByID — тем же принципом, что MetadataObjectByID, для поля
// (query_reference.member_id, register_access не ссылается на member — там
// только object_id).
func (tx *ReadTx) MetadataMemberByID(id int64) (MetadataMemberRow, bool, error) {
	if err := tx.check(); err != nil {
		return MetadataMemberRow{}, false, err
	}
	var r MetadataMemberRow
	var types sql.NullString
	var indexed, parent sql.NullInt64
	err := tx.c.sc.QueryRowContext(tx.ctx, `SELECT id,object_id,origin_file_id,kind,name_norm,name_display,types,indexed,parent_member
		FROM metadata_member WHERE id=?`, id).
		Scan(&r.ID, &r.ObjectID, &r.OriginFileID, &r.Kind, &r.NameNorm, &r.NameDisplay, &types, &indexed, &parent)
	if errors.Is(err, sql.ErrNoRows) {
		return MetadataMemberRow{}, false, nil
	}
	if err != nil {
		return MetadataMemberRow{}, false, err
	}
	r.TypesJSON, r.Indexed, r.ParentMember = types.String, indexed.Int64 != 0, parent.Int64
	return r, true, nil
}

// FormRow — одна строка form (только identity — аспекты в form_declaration/
// form_structure, здесь не нужны отдельно: get_object/get_form_handlers
// интересует, что форма существует и её имя, а не откуда она собрана).
type FormRow struct {
	ID            int64
	OwnerObjectID int64
	NameNorm      string
	NameDisplay   string
}

// FormsByOwner перечисляет формы объекта метаданных.
func (tx *ReadTx) FormsByOwner(ownerObjectID int64) ([]FormRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT id,owner_object_id,name_norm,name_display
		FROM form WHERE owner_object_id=? ORDER BY name_norm`, ownerObjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FormRow
	for rows.Next() {
		var r FormRow
		var owner sql.NullInt64
		if err := rows.Scan(&r.ID, &owner, &r.NameNorm, &r.NameDisplay); err != nil {
			return nil, err
		}
		r.OwnerObjectID = owner.Int64
		out = append(out, r)
	}
	return out, rows.Err()
}

// FormByOwnerAndName находит одну форму объекта по нормализованному имени —
// get_form_handlers form=<имя>.
func (tx *ReadTx) FormByOwnerAndName(ownerObjectID int64, nameNorm string) (FormRow, bool, error) {
	if err := tx.check(); err != nil {
		return FormRow{}, false, err
	}
	var r FormRow
	var owner sql.NullInt64
	err := tx.c.sc.QueryRowContext(tx.ctx, `SELECT id,owner_object_id,name_norm,name_display
		FROM form WHERE owner_object_id=? AND name_norm=?`, ownerObjectID, nameNorm).
		Scan(&r.ID, &owner, &r.NameNorm, &r.NameDisplay)
	if errors.Is(err, sql.ErrNoRows) {
		return FormRow{}, false, nil
	}
	if err != nil {
		return FormRow{}, false, err
	}
	r.OwnerObjectID = owner.Int64
	return r, true, nil
}

// FormCommandRow — одна строка form_command.
type FormCommandRow struct {
	ID           int64
	FormID       int64
	OriginFileID int64
	NameNorm     string
	NameDisplay  string
	ActionNorm   string
}

// FormCommands перечисляет команды формы.
func (tx *ReadTx) FormCommands(formID int64) ([]FormCommandRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT id,form_id,origin_file_id,name_norm,name_display,action_norm
		FROM form_command WHERE form_id=? ORDER BY name_norm`, formID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FormCommandRow
	for rows.Next() {
		var r FormCommandRow
		var action sql.NullString
		if err := rows.Scan(&r.ID, &r.FormID, &r.OriginFileID, &r.NameNorm, &r.NameDisplay, &action); err != nil {
			return nil, err
		}
		r.ActionNorm = action.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// EventSubscriptionRow — одна строка event_subscription.
type EventSubscriptionRow struct {
	ID              int64
	ComponentID     string
	NameNorm        string
	NameDisplay     string
	SourceKind      string
	SourceNameNorm  string
	Event           string
	HandlerNameNorm string
	HandlerSymbolID int64
	OriginFileID    int64
	Resolution      string
	Layer           string
}

// EventSubscriptionsBySourceNames отдаёт подписки, чей source_name_norm
// совпадает с ЛЮБЫМ из candidates. source_name_norm хранит нормализованный
// СЫРОЙ текст источника платформы («catalogobject.товары» для конкретного
// типа, «documentobject» для голого вида — R30.1, обе формы уже слиты в одну
// колонку publishEventSubscription), НЕ голое имя объекта метаданных —
// сравнивать его с metadata_object.name_norm напрямую нельзя, поэтому
// кандидатов ("<mtype>object.<имя>" и "<mtype>object" для голого вида)
// строит вызывающий — он знает MType объекта.
//
// Вызывающих СЕГОДНЯ ДВА, и правило построения кандидатов живёт в двух
// копиях: internal/app (meta.go, get_object) и internal/retrieve
// (expand2.go:subscriptionSourceCandidates, интент posting). Вторая копия
// заведена сознательно — граница internal/arch запрещает retrieve
// импортировать app, — но это именно копия: правя одну, правь обе, иначе
// один и тот же объект получит разный набор подписок в двух инструментах.
func (tx *ReadTx) EventSubscriptionsBySourceNames(candidates []string) ([]EventSubscriptionRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	args := make([]any, len(candidates))
	for i, c := range candidates {
		args[i] = c
	}
	rows, err := tx.c.query(tx.ctx, `SELECT id,component_id,name_norm,name_display,source_kind,source_name_norm,
		event,handler_name_norm,handler_symbol_id,origin_file_id,resolution,layer
		FROM event_subscription WHERE source_name_norm IN `+inClause(len(candidates))+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventSubscriptionRow
	for rows.Next() {
		var r EventSubscriptionRow
		var src sql.NullString
		var handlerSym sql.NullInt64
		if err := rows.Scan(&r.ID, &r.ComponentID, &r.NameNorm, &r.NameDisplay, &r.SourceKind, &src,
			&r.Event, &r.HandlerNameNorm, &handlerSym, &r.OriginFileID, &r.Resolution, &r.Layer); err != nil {
			return nil, err
		}
		r.SourceNameNorm, r.HandlerSymbolID = src.String, handlerSym.Int64
		out = append(out, r)
	}
	return out, rows.Err()
}

// ScheduledJobRow — одна строка scheduled_job.
type ScheduledJobRow struct {
	ID              int64
	ComponentID     string
	NameNorm        string
	NameDisplay     string
	MethodNameNorm  string
	HandlerSymbolID int64
	OriginFileID    int64
	Use             bool
	Predefined      bool
	Resolution      string
	Layer           string
}

// ScheduledJobsByName ищет регламентное задание по имени внутри компонента:
// scheduled_job не ссылается на произвольный metadata_object (схема раздела
// 15 не несёт такого FK), совпадение возможно только когда сам запрошенный
// объект — ScheduledJob (component_id+name_norm совпадают с его собственной
// строкой metadata_object), другого способа «регламентные задания,
// ссылающиеся на объект» эта схема не даёт — упрощение, названо в handoff.
func (tx *ReadTx) ScheduledJobsByName(componentID, nameNorm string) ([]ScheduledJobRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT id,component_id,name_norm,name_display,method_name_norm,
		handler_symbol_id,origin_file_id,use_flag,predefined,resolution,layer
		FROM scheduled_job WHERE component_id=? AND name_norm=? ORDER BY id`, componentID, nameNorm)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledJobRow
	for rows.Next() {
		var r ScheduledJobRow
		var handlerSym sql.NullInt64
		var use, predefined int64
		if err := rows.Scan(&r.ID, &r.ComponentID, &r.NameNorm, &r.NameDisplay, &r.MethodNameNorm,
			&handlerSym, &r.OriginFileID, &use, &predefined, &r.Resolution, &r.Layer); err != nil {
			return nil, err
		}
		r.HandlerSymbolID, r.Use, r.Predefined = handlerSym.Int64, use != 0, predefined != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// RoleRightRow — одно право роли, уже с именем роли (join role) — get_object
// показывает «роли с правами на него», не голый role_id.
type RoleRightRow struct {
	RoleID           int64
	RoleComponentID  string
	RoleNameNorm     string
	RoleNameDisplay  string
	RoleLayer        string
	ObjectID         int64
	ObjectNameNorm   string
	RightName        string
	Value            bool
	RLS              string
	SetForNewObjects bool
	OriginFileID     int64
}

// RoleRightsByObjectID отдаёт права ВСЕХ ролей на объект метаданных по его
// node.id — role_right.object_id уже резолвится при публикации
// (resolveRoleObjectNode, index/publishmeta2.go) через ТОТ ЖЕ identity_key,
// что и сам объект, поэтому это надёжнее совпадения по имени:
// role_right.object_name_norm хранит нормализованный ПОЛНЫЙ текст из
// Rights.xml ("справочник.товары"), не голое имя объекта, и сравнивать его с
// metadata_object.name_norm впрямую было бы систематическим промахом. ИЛИ-логика
// и set_for_new_objects (R30.2) остаются как в role_right (raw факты, включая
// value=false), решение по ним — дело вызывающего (та же семантика, что
// rightsaudit.go).
func (tx *ReadTx) RoleRightsByObjectID(objectID int64) ([]RoleRightRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT rr.role_id, r.component_id, r.name_norm, r.name_display, r.layer,
		rr.object_id, rr.object_name_norm, rr.right_name, rr.value, rr.rls, rr.set_for_new_objects, rr.origin_file_id
		FROM role_right rr JOIN role r ON r.id = rr.role_id
		WHERE rr.object_id=? ORDER BY r.name_norm, rr.right_name`, objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoleRightRow
	for rows.Next() {
		var r RoleRightRow
		var objectID sql.NullInt64
		var rls sql.NullString
		var value, setForNew int64
		if err := rows.Scan(&r.RoleID, &r.RoleComponentID, &r.RoleNameNorm, &r.RoleNameDisplay, &r.RoleLayer,
			&objectID, &r.ObjectNameNorm, &r.RightName, &value, &rls, &setForNew, &r.OriginFileID); err != nil {
			return nil, err
		}
		r.ObjectID, r.Value, r.RLS, r.SetForNewObjects = objectID.Int64, value != 0, rls.String, setForNew != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// HandlerBindingRow — одна строка handler_binding.
type HandlerBindingRow struct {
	ID              int64
	FormID          int64
	Source          string
	Event           string
	HandlerNameNorm string
	HandlerSymbolID int64
	OriginFileID    int64
	Resolution      string
}

// HandlerBindingsByForm перечисляет привязки обработчиков формы, включая
// unresolved (R43.1: обработчик объявлен, но не найден в модуле — остаётся
// строкой, не пропадает).
func (tx *ReadTx) HandlerBindingsByForm(formID int64) ([]HandlerBindingRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT id,form_id,source,event,handler_name_norm,handler_symbol_id,origin_file_id,resolution
		FROM handler_binding WHERE form_id=? ORDER BY source, event`, formID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HandlerBindingRow
	for rows.Next() {
		var r HandlerBindingRow
		var handlerSym sql.NullInt64
		if err := rows.Scan(&r.ID, &r.FormID, &r.Source, &r.Event, &r.HandlerNameNorm, &handlerSym,
			&r.OriginFileID, &r.Resolution); err != nil {
			return nil, err
		}
		r.HandlerSymbolID = handlerSym.Int64
		out = append(out, r)
	}
	return out, rows.Err()
}

// SymbolRow и SourceFileRow, вместе с SymbolByID/SourceFileByID, уже
// объявлены в read_symbol.go (тикет 11 — тот же шов ReadTx, оказался нужен
// первым ему): get_form_handlers/find_queries_using/find_register_writes
// переиспользуют их, не заводя вторую проекцию тех же таблиц.
