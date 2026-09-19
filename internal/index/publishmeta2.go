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
func publishEventSubscription(tx *store.WriteTx, ts *txState, rel string, fileID int64, hp *handlerPlan[store.EventSubscription]) error {
	row := hp.row
	row.OriginFileID = fileID
	id, err := handlerSymbolID(tx, ts, hp.handlerKey)
	if err != nil {
		return fmt.Errorf("обработчик подписки %s: %w", rel, err)
	}
	row.HandlerSymbolID = id
	if err := tx.InsertEventSubscription(row); err != nil {
		return err
	}
	ts.counts.eventSub++
	return nil
}

// publishScheduledJob вставляет регламентное задание. Метод
// ("CommonModule.Имя.Процедура") резолвится так же, как обработчик подписки.
func publishScheduledJob(tx *store.WriteTx, ts *txState, rel string, fileID int64, hp *handlerPlan[store.ScheduledJob]) error {
	row := hp.row
	row.OriginFileID = fileID
	id, err := handlerSymbolID(tx, ts, hp.handlerKey)
	if err != nil {
		return fmt.Errorf("обработчик задания %s: %w", rel, err)
	}
	row.HandlerSymbolID = id
	if err := tx.InsertScheduledJob(row); err != nil {
		return err
	}
	ts.counts.scheduledJob++
	return nil
}

// publishRole вставляет identity роли (аспект — её собственный XML). Роль не
// node-сущность (interfaces.md, таск 03): identity — имя внутри компонента.
// Строкой роли владеет этот файл: публикация Rights.xml её не переписывает
// (store.RoleForRights).
func publishRole(tx *store.WriteTx, rel string, fileID, objID int64, role *store.Role) error {
	row := *role
	row.ObjectID, row.FileID = objID, fileID
	if _, err := tx.EnsureRole(row); err != nil {
		return fmt.Errorf("role %s: %w", rel, err)
	}
	return nil
}

// publishRoleRights вставляет права роли (Rights.xml, отдельный файл от
// самой роли) СЫРЫМИ фактами, включая value=false строки — ИЛИ-агрегация
// (resolve.EffectiveRoleObjectRights) остаётся делом читающего слоя (app,
// таск 12+): здесь её вызывать не для чего, раз ничего из её результата не
// публикуется отдельной таблицей, а схема store для эффективных прав
// отдельного места не резервирует. Роль уже есть (её XML опубликован этой
// или прошлой транзакцией): берётся как есть. Нет (XML роли в выгрузке нет):
// создаётся на file_id этого Rights.xml, как её создала бы и чистая
// пересборка.
func publishRoleRights(tx *store.WriteTx, ts *txState, rel string, fileID int64, rp *roleRightsPlan) error {
	role := rp.role
	role.FileID = fileID
	roleID, err := tx.RoleForRights(role)
	if err != nil {
		return fmt.Errorf("role (rights) %s: %w", rel, err)
	}

	for _, obj := range rp.objects {
		objID := resolveRoleObjectNode(tx, ts, obj.objectKey)
		for _, right := range obj.rights {
			right.RoleID, right.ObjectID, right.OriginFileID = roleID, objID, fileID
			if err := tx.InsertRoleRight(right); err != nil {
				return fmt.Errorf("role_right %s/%s/%s: %w", rel, right.ObjectNameNorm, right.RightName, err)
			}
			ts.counts.roleRight++
		}
	}
	return nil
}

// resolveRoleObjectNode переводит ключ объекта права (план строит его из
// "Catalog.Товары" в Rights.xml, см. roleObjectKey) в node_id объекта
// метаданных, если он проиндексирован: join уже разобранных фактов по
// identity, не второй парсер. 0, если объект не нашёлся (soft target, §15:
// право без объекта не теряется).
func resolveRoleObjectNode(tx *store.WriteTx, ts *txState, objKey string) int64 {
	if objKey == "" {
		return 0
	}
	if id, ok, err := ts.nodes.lookup(tx, objKey); err == nil && ok {
		return id
	}
	return 0
}

// handlerSymbolID отдаёт id узла символа-обработчика по ключу из плана
// (пустой ключ: resolveModuleHandler обработчик не разрешил, 0 это NULL) тем
// же путём, что и обычные ссылки: ts.nodes для своих файлов транзакции без
// лишнего SELECT, tx.NodeID для чужих.
func handlerSymbolID(tx *store.WriteTx, ts *txState, key string) (int64, error) {
	if key == "" {
		return 0, nil
	}
	id, ok, err := ts.nodes.lookup(tx, key)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	return id, nil
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
