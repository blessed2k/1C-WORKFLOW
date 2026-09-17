package index

import (
	"fmt"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// publishEventSubscription вставляет подписку на событие. Обработчик
// ("CommonModule.Имя.Процедура", meta/facts.go) резолвится локально —
// internal/resolve не даёт готовой Derive-функции для этой формы (только для
// обработчиков формы, DeriveHandlerBinding), а сама резолюция — join уже
// разобранных фактов по env (resolveModuleHandler), не второй резолвер.
func publishEventSubscription(tx *store.WriteTx, in publishInput, ts *txState, rel string, fileID int64, rec *fileRecord) error {
	s := rec.metaFacts.Subscription
	uid, res := resolveModuleHandler(in.env, s.HandlerRaw)
	handlerSymbolID, err := symbolNodeIDIfResolved(tx, ts, uid, res)
	if err != nil {
		return fmt.Errorf("обработчик подписки %s: %w", rel, err)
	}
	sourceKind, sourceNameNorm := "", ""
	if len(s.Sources) > 0 {
		sourceKind = string(s.Sources[0].Kind)
		sourceNameNorm = domain.NormalizeName(s.Sources[0].Name)
	}
	if err := tx.InsertEventSubscription(store.EventSubscription{
		ComponentID: string(in.component), NameNorm: s.NameNorm, NameDisplay: s.NameDisplay,
		SourceKind: sourceKind, SourceNameNorm: sourceNameNorm, Event: s.Event,
		HandlerNameNorm: domain.NormalizeName(s.HandlerRaw), HandlerSymbolID: handlerSymbolID,
		OriginFileID: fileID, Resolution: string(res), Layer: layerName(in.layer),
	}); err != nil {
		return err
	}
	ts.counts.eventSub++
	return nil
}

// publishScheduledJob вставляет регламентное задание. Метод
// ("CommonModule.Имя.Процедура") резолвится так же, как обработчик подписки.
func publishScheduledJob(tx *store.WriteTx, in publishInput, ts *txState, rel string, fileID int64, rec *fileRecord) error {
	j := rec.metaFacts.ScheduledJob
	uid, res := resolveModuleHandler(in.env, j.MethodRaw)
	handlerSymbolID, err := symbolNodeIDIfResolved(tx, ts, uid, res)
	if err != nil {
		return fmt.Errorf("обработчик задания %s: %w", rel, err)
	}
	if err := tx.InsertScheduledJob(store.ScheduledJob{
		ComponentID: string(in.component), NameNorm: j.NameNorm, NameDisplay: j.NameDisplay,
		MethodNameNorm: domain.NormalizeName(j.MethodRaw), HandlerSymbolID: handlerSymbolID,
		OriginFileID: fileID, Use: j.Use, Predefined: j.Predefined,
		Resolution: string(res), Layer: layerName(in.layer),
	}); err != nil {
		return err
	}
	ts.counts.scheduledJob++
	return nil
}

// publishRole вставляет identity роли (аспект — её собственный XML). Роль не
// node-сущность (interfaces.md, таск 03): identity — имя внутри компонента.
// ts.roleFile запоминает file_id ИМЕННО этого файла, чтобы publishRoleRights
// (другой файл, Rights.xml) не перезаписал его чужим id — EnsureRole
// безусловно перезаписывает file_id тем, что ему передали.
func publishRole(tx *store.WriteTx, in publishInput, ts *txState, rel string, fileID, objID int64, rec *fileRecord) error {
	_, err := tx.EnsureRole(store.Role{
		ComponentID: string(in.component), NameNorm: rec.metaFacts.Role.NameNorm,
		NameDisplay: rec.metaFacts.Role.NameDisplay, ObjectID: objID, FileID: fileID, Layer: layerName(in.layer),
	})
	if err != nil {
		return fmt.Errorf("role %s: %w", rel, err)
	}
	ts.roleFile[roleCacheKey(in.component, rec.metaFacts.Role.NameNorm)] = fileID
	return nil
}

// publishRoleRights вставляет права роли (Rights.xml, отдельный файл от
// самой роли) СЫРЫМИ фактами, включая value=false строки — ИЛИ-агрегация
// (resolve.EffectiveRoleObjectRights) остаётся делом читающего слоя (app,
// таск 12+): здесь её вызывать не для чего, раз ничего из её результата не
// публикуется отдельной таблицей, а схема store для эффективных прав
// отдельного места не резервирует. Если роль уже встретилась в ЭТОЙ ЖЕ
// транзакции (publishRole), используется её настоящий file_id. Если роль в
// этой транзакции не публиковалась (инкремент тронул только Rights.xml) —
// узкий случай, file_id остаётся на Rights.xml вместо собственного XML роли
// (упрощение: store не даёт прочитать существующий file_id роли без второго
// SQL-слоя вне себя).
func publishRoleRights(tx *store.WriteTx, in publishInput, ts *txState, rel string, fileID int64, rec *fileRecord) error {
	rr := rec.metaFacts.RoleRights
	key := roleCacheKey(in.component, rr.RoleNameNorm)
	roleFileID, ok := ts.roleFile[key]
	if !ok {
		roleFileID = fileID
	}
	roleID, err := tx.EnsureRole(store.Role{
		ComponentID: string(in.component), NameNorm: rr.RoleNameNorm,
		NameDisplay: rr.RoleNameDisplay, FileID: roleFileID, Layer: layerName(in.layer),
	})
	if err != nil {
		return fmt.Errorf("role (rights) %s: %w", rel, err)
	}
	ts.roleFile[key] = roleFileID

	for _, obj := range rr.Objects {
		objID := resolveRoleObjectNode(tx, in, ts, obj.ObjectNameRaw)
		for _, right := range obj.Rights {
			if err := tx.InsertRoleRight(store.RoleRight{
				RoleID: roleID, ObjectID: objID, ObjectNameNorm: domain.NormalizeName(obj.ObjectNameRaw),
				RightName: right.Name, Value: right.Value, RLS: rlsToText(right.RLS),
				SetForNewObject: rr.SetForNewObjects, OriginFileID: fileID,
			}); err != nil {
				return fmt.Errorf("role_right %s/%s/%s: %w", rel, obj.ObjectNameRaw, right.Name, err)
			}
			ts.counts.roleRight++
		}
	}
	return nil
}

func roleCacheKey(component domain.ComponentID, nameNorm string) string {
	return string(component) + "\x00" + nameNorm
}

// resolveRoleObjectNode переводит "Catalog.Товары" (Rights.xml,
// ObjectNameRaw) в node_id объекта метаданных, если он проиндексирован —
// join уже разобранных фактов по identity, не второй парсер. 0, если объект
// не нашёлся (soft target, §15: право без объекта не теряется).
func resolveRoleObjectNode(tx *store.WriteTx, in publishInput, ts *txState, objectFullName string) int64 {
	parts := strings.SplitN(objectFullName, ".", 2)
	if len(parts) < 2 {
		return 0
	}
	objKey := metadataObjectIdentityKey(in.component, parts[0], domain.NormalizeName(parts[1]))
	if id, ok, err := ts.nodes.lookup(tx, objKey); err == nil && ok {
		return id
	}
	return 0
}

// rlsToText сериализует RLS-ограничения права в компактный текст —
// упрощение: "поле1,поле2: условие; ..." одной строкой вместо отдельной
// таблицы (в схеме store её для этого нет, role_right.rls — TEXT).
func rlsToText(rls []meta.RLSRestrictionFact) string {
	if len(rls) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rls))
	for _, r := range rls {
		if len(r.Fields) == 0 {
			parts = append(parts, r.Condition)
			continue
		}
		parts = append(parts, strings.Join(r.Fields, ",")+": "+r.Condition)
	}
	return strings.Join(parts, "; ")
}

// resolveModuleHandler разбирает "CommonModule.Имя.Процедура" (формат
// EventSubscriptionFact.HandlerRaw/ScheduledJobFact.MethodRaw, meta/facts.go)
// и ищет экспортный метод в общем модуле через уже построенный Env — та же
// область поиска, что и у квалифицированного вызова §19.1 п.1, применённая к
// обработчику вместо BSL-ссылки.
func resolveModuleHandler(env resolve.Env, raw string) (domain.SymbolUID, domain.Resolution) {
	parts := strings.SplitN(raw, ".", 3)
	if len(parts) != 3 {
		return "", domain.ResolutionUnresolved
	}
	moduleNameNorm := domain.NormalizeName(parts[1])
	methodNameNorm := domain.NormalizeName(parts[2])
	mod, ok := env.CommonModuleByName(moduleNameNorm)
	if !ok {
		return "", domain.ResolutionUnresolved
	}
	var matches []domain.SymbolUID
	for _, s := range mod.Symbols {
		if s.Export && (s.Kind == domain.SymbolProcedure || s.Kind == domain.SymbolFunction) && s.NameNorm == methodNameNorm {
			matches = append(matches, s.UID)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], domain.ResolutionResolved
	case 0:
		return "", domain.ResolutionUnresolved
	default:
		return "", domain.ResolutionAmbiguous
	}
}

// symbolNodeIDIfResolved отдаёт id узла символа, если resolveModuleHandler
// разрешил обработчик; 0 (NULL) иначе — тем же путём, что и обычные ссылки
// (ts.nodes: свои файлы транзакции без лишнего SELECT, чужие — через tx.NodeID).
func symbolNodeIDIfResolved(tx *store.WriteTx, ts *txState, uid domain.SymbolUID, res domain.Resolution) (int64, error) {
	if res != domain.ResolutionResolved {
		return 0, nil
	}
	id, ok, err := ts.nodes.lookupSymbol(tx, uid)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	return id, nil
}
