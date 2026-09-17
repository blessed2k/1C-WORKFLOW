package resolve

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
)

// TestEffectiveRoleObjectRightsORLogic — ИЛИ-логика: объект упомянут в
// Rights.xml роли двумя блоками (реальный случай выгрузки — раздельные
// блоки для разных групп прав одного объекта), права из ОБОИХ блоков
// действуют, value=false пропускается, а не превращается в отказ, отменяя
// true из другого блока.
func TestEffectiveRoleObjectRightsORLogic(t *testing.T) {
	fact := meta.RoleRightsFact{
		RoleNameNorm: "менеджер",
		Objects: []meta.RoleRightObjectFact{
			{
				ObjectNameRaw: "Catalog.Товары",
				Rights: []meta.RoleRightEntryFact{
					{Name: "Read", Value: true},
					{Name: "Delete", Value: false},
				},
			},
			{
				ObjectNameRaw: "Catalog.Товары",
				Rights: []meta.RoleRightEntryFact{
					{Name: "Update", Value: true, RLS: []meta.RLSRestrictionFact{{Condition: "Отдел = &ТекущийОтдел"}}},
				},
			},
			{
				ObjectNameRaw: "Catalog.Другой",
				Rights: []meta.RoleRightEntryFact{
					{Name: "Read", Value: true},
				},
			},
		},
	}

	rights, listed := EffectiveRoleObjectRights(fact, "Catalog.Товары")
	if !listed {
		t.Fatal("listed = false, объект упомянут в Rights.xml")
	}
	names := map[string]int{}
	for _, r := range rights {
		names[r.Name]++
	}
	if names["Read"] != 1 {
		t.Errorf("Read: %d, ожидалось 1", names["Read"])
	}
	if names["Update"] != 1 {
		t.Errorf("Update: %d, ожидалось 1 (из второго блока, ИЛИ-логика)", names["Update"])
	}
	if names["Delete"] != 0 {
		t.Errorf("Delete: %d, ожидалось 0 — value=false не право", names["Delete"])
	}
	if names["Read"]+names["Update"]+names["Delete"] != len(rights) {
		t.Errorf("лишние права из Catalog.Другой просочились: %+v", rights)
	}

	var updateRLS []meta.RLSRestrictionFact
	for _, r := range rights {
		if r.Name == "Update" {
			updateRLS = r.RLS
		}
	}
	if len(updateRLS) != 1 || updateRLS[0].Condition != "Отдел = &ТекущийОтдел" {
		t.Errorf("RLS для Update = %+v, ожидалось одно ограничение по отделу", updateRLS)
	}
}

// TestEffectiveRoleObjectRightsNotListed — объект не упомянут вовсе:
// listed=false, права пустые (setForNewObjects проверяет вызывающий по
// fact.SetForNewObjects напрямую — не дело этой функции).
func TestEffectiveRoleObjectRightsNotListed(t *testing.T) {
	fact := meta.RoleRightsFact{SetForNewObjects: true}
	rights, listed := EffectiveRoleObjectRights(fact, "Catalog.Товары")
	if listed {
		t.Error("listed = true, объект нигде не упомянут")
	}
	if len(rights) != 0 {
		t.Errorf("rights = %+v, ожидалось пусто", rights)
	}
}
