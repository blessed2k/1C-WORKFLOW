package resolve

import "github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"

// RoleObjectRight — одно право, предоставленное ролью на объекте, после
// ИЛИ-логики.
type RoleObjectRight struct {
	Name string
	RLS  []meta.RLSRestrictionFact
}

// EffectiveRoleObjectRights сворачивает сырые факты одной роли
// (parse/meta.RoleRightsFact — "включая value=false; ИЛИ-логика — дело
// резолвера") в права, действующие на объекте: Rights.xml роли
// может упоминать один объект несколькими <Object>-блоками (например,
// отдельно для самого объекта и для его табличных частей/реквизитов
// адресации), и семантика платформы — ИЛИ по всем таким блокам, не
// "последний записанный побеждает". Перенос семантики
// internal/source/rightsaudit.go:roleRights, закреплённый здесь тестом.
//
// value=false строки пропускаются как есть (право явно НЕ предоставлено
// этой строкой — молчание, а не отказ). listed сообщает, встретился ли
// объект в Rights.xml роли вообще: при !listed и fact.SetForNewObjects
// права роли на объекте выгрузка не фиксирует — то самое «нет в списке не
// равно нет доступа» (internal/source/rightsaudit.go).
func EffectiveRoleObjectRights(fact meta.RoleRightsFact, objectFullName string) (rights []RoleObjectRight, listed bool) {
	for _, obj := range fact.Objects {
		if obj.ObjectNameRaw != objectFullName {
			continue
		}
		listed = true
		for _, r := range obj.Rights {
			if !r.Value {
				continue
			}
			rights = append(rights, RoleObjectRight{Name: r.Name, RLS: r.RLS})
		}
	}
	return rights, listed
}
