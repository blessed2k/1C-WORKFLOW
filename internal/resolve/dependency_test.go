package resolve

import "testing"

// TestDeriveDependencyEdgesFieldTypedBy — критерий из тела таска 08: "поле
// типизировано объектом" -> dependency_edge. Вход построен вручную по
// формату реального XML (cfg:CatalogRef.Имя, см. Documents/*.xml выгрузки
// ut_demo), ожидаемое ребро посчитано по этому входу вручную, а не взято из
// вывода DeriveDependencyEdges.
func TestDeriveDependencyEdgesFieldTypedBy(t *testing.T) {
	env := mustEnv(t, EnvInput{
		Component: testComponent,
		Objects: []ObjectRef{
			{MType: "Catalog", NameNorm: "контрагенты", IdentityKey: "metadata:Catalog:контрагенты"},
		},
		Members: []MemberRef{
			{
				ObjectMType: "Document", ObjectNameNorm: "реализациятоваровуслуг",
				NameNorm:    "контрагент",
				Types:       []string{"cfg:CatalogRef.Контрагенты"},
				IdentityKey: "member:Document:реализациятоваровуслуг:контрагент",
			},
			{
				// строковый реквизит — не ссылочный тип, ребра быть не должно.
				ObjectMType: "Document", ObjectNameNorm: "реализациятоваровуслуг",
				NameNorm:    "комментарий",
				Types:       []string{"xs:string"},
				IdentityKey: "member:Document:реализациятоваровуслуг:комментарий",
			},
			{
				// ссылка на объект, которого нет в Env.Objects — мягкая цель, ребро не строим.
				ObjectMType: "Document", ObjectNameNorm: "реализациятоваровуслуг",
				NameNorm:    "склад",
				Types:       []string{"cfg:CatalogRef.Склады"},
				IdentityKey: "member:Document:реализациятоваровуслуг:склад",
			},
		},
	}, nil)

	edges := DeriveDependencyEdges(env)
	if len(edges) != 1 {
		t.Fatalf("рёбер = %d, ожидалось 1: %+v", len(edges), edges)
	}
	e := edges[0]
	if e.Kind != DepFieldTypedBy {
		t.Errorf("kind = %s, ожидалось %s", e.Kind, DepFieldTypedBy)
	}
	if e.FromKey != "member:Document:реализациятоваровуслуг:контрагент" {
		t.Errorf("fromKey = %q, ожидался ключ реквизита Контрагент", e.FromKey)
	}
	if e.ToKey != "metadata:Catalog:контрагенты" {
		t.Errorf("toKey = %q, ожидался ключ Catalog/контрагенты", e.ToKey)
	}
}

// TestDeriveDependencyEdgesEmpty — нет типизированных ссылочных полей —
// пустой срез, не nil-паника и не мусор.
func TestDeriveDependencyEdgesEmpty(t *testing.T) {
	env := mustEnv(t, EnvInput{Component: testComponent}, nil)
	edges := DeriveDependencyEdges(env)
	if len(edges) != 0 {
		t.Errorf("edges = %+v, ожидалось пусто", edges)
	}
}
