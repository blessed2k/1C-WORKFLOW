package retrieve

import (
	"context"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// Фикстура объекта с заимствованием для add-attribute и rights (ADR-035).
// Документ ЗаказПокупателя живёт в cfg; ext-a заимствует его (своя строка
// metadata_object) и добавляет СВОЙ реквизит, СВОЮ форму и роль расширения с
// правом и RLS на заимствованный объект. Права роли расширения индекс
// связывает со строкой объекта в компоненте роли (resolveRoleObjectNode), то
// есть с заимствованной строкой, а не с базовой: по базовому анкеру их не
// видно без наложения.

const (
	effAddAttrTask = "Добавь реквизит Склад в документ ЗаказПокупателя"
	effRightsTask  = "Пользователь не видит документ ЗаказПокупателя, нужен разбор прав и RLS"
)

func seedEffObjectFixture(t *testing.T, st *store.Store) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		seedEffComponents(t, tx)
		for _, comp := range []string{"cfg", effExt} {
			oid := seedEffObject(t, tx, comp, "Document", "ЗаказПокупателя")
			decl, ok, err := tx.SourceFileID(comp, declPath("Document", "ЗаказПокупателя"))
			if err != nil || !ok {
				t.Fatalf("SourceFileID(%s): ok=%v err=%v", comp, ok, err)
			}
			member, form, role, right, rls := "Комментарий", "ФормаДокумента", "Менеджер", "Чтение", ""
			if comp == effExt {
				member, form, role, right, rls = "Расш_Приоритет", "Расш_ФормаСписка", "Расш_Оператор", "Изменение", "Организация = &ТекущаяОрганизация"
			}
			if _, err := tx.InsertMetadataMember(store.MetadataMember{
				IdentityKey: comp + "\x00member\x00" + member, ComponentID: comp, ObjectID: oid, OriginFileID: decl,
				Kind: "Attribute", NameNorm: strings.ToLower(member), NameDisplay: member,
			}); err != nil {
				return err
			}
			fid, err := tx.EnsureForm(store.Form{
				IdentityKey: comp + "\x00form\x00" + form, ComponentID: comp, OwnerObjectID: oid,
				NameNorm: strings.ToLower(form), NameDisplay: form,
			})
			if err != nil {
				return err
			}
			if err := tx.PutFormDeclaration(fid, decl); err != nil {
				return err
			}
			layer := "base"
			if comp != "cfg" {
				layer = comp
			}
			rid, err := tx.EnsureRole(store.Role{ComponentID: comp, NameNorm: strings.ToLower(role), NameDisplay: role, FileID: decl, Layer: layer})
			if err != nil {
				return err
			}
			if err := tx.InsertRoleRight(store.RoleRight{
				RoleID: rid, ObjectID: oid, ObjectNameNorm: "document.заказпокупателя", RightName: right,
				Value: true, RLS: rls, OriginFileID: decl,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seedEffObjectFixture: %v", err)
	}
}

func factsOf(r Result, category string) []Fact {
	var out []Fact
	for _, f := range r.Facts {
		if f.Category == category {
			out = append(out, f)
		}
	}
	return out
}

func factByDisplay(facts []Fact, display string) *Fact {
	for i := range facts {
		if facts[i].Display == display {
			return &facts[i]
		}
	}
	return nil
}

// TestEffectiveAddAttributeBorrowedLayers: add-attribute под view=effective
// несёт заимствованный объект расширения: его структуру с реквизитами
// расширения, его формы и права ролей расширения, каждый факт со своим
// слоем. raw по базовому анкеру (componentHints=cfg) их не несёт. Без
// componentHints raw видит обе строки объекта двумя анкерами, но структура
// заимствования в нём теряется: ключ кандидата структуры один на имя.
func TestEffectiveAddAttributeBorrowedLayers(t *testing.T) {
	st := openFixtureStore(t)
	seedEffObjectFixture(t, st)
	base := []string{"cfg"}

	raw := buildFor(t, st, Request{Task: effAddAttrTask, ProjectID: "p", ComponentHints: base})
	if raw.Intent.Primary != IntentAddAttribute {
		t.Fatalf("Intent.Primary = %q, want %q", raw.Intent.Primary, IntentAddAttribute)
	}
	for _, m := range raw.MetadataSummaries {
		if m.Component == effExt {
			t.Fatalf("view=raw по базовому анкеру не несёт структуры заимствования: %+v", raw.MetadataSummaries)
		}
	}
	if factByDisplay(factsOf(raw, "forms"), "Расш_ФормаСписка") != nil || factByDisplay(factsOf(raw, "rights"), "Расш_Оператор") != nil {
		t.Fatalf("view=raw по базовому анкеру не несёт форм и ролей расширения: %+v", raw.Facts)
	}
	requireCoverageStatus(t, raw, "forms", CompleteInline)

	for _, hints := range [][]string{base, nil} {
		eff := buildFor(t, st, Request{Task: effAddAttrTask, ProjectID: "p", View: "effective", ComponentHints: hints})
		if hasWarning(eff, "effective_view_partial_coverage") {
			t.Fatalf("add-attribute под effective не строится как raw, предупреждения быть не должно: %+v", eff.Warnings)
		}
		var borrowed *MetadataSummary
		for i := range eff.MetadataSummaries {
			if eff.MetadataSummaries[i].Component == effExt {
				borrowed = &eff.MetadataSummaries[i]
			}
		}
		if borrowed == nil || len(borrowed.Members) != 1 || borrowed.Members[0].Name != "Расш_Приоритет" ||
			!strings.Contains(borrowed.WhyIncluded, "заимствован") {
			t.Fatalf("hints=%v: effective обязан нести структуру заимствования %s с реквизитом расширения: %+v", hints, effExt, eff.MetadataSummaries)
		}
		if f := factByDisplay(factsOf(eff, "forms"), "Расш_ФормаСписка"); f == nil || f.Component != effExt {
			t.Fatalf("hints=%v: effective обязан нести форму расширения со слоем %s: %+v", hints, effExt, eff.Facts)
		}
		if f := factByDisplay(factsOf(eff, "rights"), "Расш_Оператор"); f == nil || f.Component != effExt {
			t.Fatalf("hints=%v: effective обязан нести роль расширения со слоем %s: %+v", hints, effExt, eff.Facts)
		}
		if f := factByDisplay(factsOf(eff, "forms"), "ФормаДокумента"); f == nil || f.Component != "cfg" {
			t.Fatalf("hints=%v: базовая форма обязана остаться: %+v", hints, eff.Facts)
		}
	}
}

// TestEffectiveAddAttributeFormsDeclaredCollected: под effective формы
// собираются по всем слоям объекта, и честная пустота называется
// complete_empty (заявление declareCollected, ADR-030); raw заявления не
// делает и остаётся missing, как было.
func TestEffectiveAddAttributeFormsDeclaredCollected(t *testing.T) {
	st := openFixtureStore(t)
	err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		seedEffComponents(t, tx)
		seedEffObject(t, tx, "cfg", "Document", "Заметка")
		seedEffObject(t, tx, effExt, "Document", "Заметка")
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	task := "Добавь реквизит Автор в документ Заметка"
	raw := buildFor(t, st, Request{Task: task, ProjectID: "p"})
	requireCoverageStatus(t, raw, "forms", Missing)
	eff := buildFor(t, st, Request{Task: task, ProjectID: "p", View: "effective"})
	requireCoverageStatus(t, eff, "forms", CompleteEmpty)
}

// TestEffectiveRightsExtensionRoles: rights под view=effective несёт роли
// расширения с их правами и RLS на заимствованный объект, со слоем
// расширения. raw по базовому анкеру (componentHints=cfg) их не несёт.
func TestEffectiveRightsExtensionRoles(t *testing.T) {
	st := openFixtureStore(t)
	seedEffObjectFixture(t, st)
	base := []string{"cfg"}

	raw := buildFor(t, st, Request{Task: effRightsTask, ProjectID: "p", ComponentHints: base})
	if raw.Intent.Primary != IntentRights {
		t.Fatalf("Intent.Primary = %q, want %q", raw.Intent.Primary, IntentRights)
	}
	if factByDisplay(factsOf(raw, "roles"), "Менеджер") == nil {
		t.Fatalf("raw: базовая роль обязана быть в ответе: %+v", raw.Facts)
	}
	if factByDisplay(factsOf(raw, "roles"), "Расш_Оператор") != nil || len(factsOf(raw, "rls")) != 0 {
		t.Fatalf("view=raw по базовому анкеру не несёт ролей и RLS расширения: %+v", raw.Facts)
	}

	for _, hints := range [][]string{base, nil} {
		eff := buildFor(t, st, Request{Task: effRightsTask, ProjectID: "p", View: "effective", ComponentHints: hints})
		if hasWarning(eff, "effective_view_partial_coverage") {
			t.Fatalf("rights под effective не строится как raw, предупреждения быть не должно: %+v", eff.Warnings)
		}
		role := factByDisplay(factsOf(eff, "roles"), "Расш_Оператор")
		if role == nil || role.Component != effExt || !strings.Contains(role.WhyIncluded, "заимствован") {
			t.Fatalf("hints=%v: effective обязан нести роль расширения со слоем %s: %+v", hints, effExt, eff.Facts)
		}
		if r := factByDisplay(factsOf(eff, "rights"), "Расш_Оператор.Изменение=true"); r == nil || r.Component != effExt {
			t.Fatalf("hints=%v: effective обязан нести право роли расширения: %+v", hints, eff.Facts)
		}
		rls := factsOf(eff, "rls")
		if len(rls) != 1 || rls[0].Component != effExt || !strings.Contains(rls[0].Detail, "ТекущаяОрганизация") {
			t.Fatalf("hints=%v: effective обязан нести RLS роли расширения: %+v", hints, rls)
		}
		if factByDisplay(factsOf(eff, "roles"), "Менеджер") == nil {
			t.Fatalf("hints=%v: базовая роль обязана остаться: %+v", hints, eff.Facts)
		}
	}
}

// TestEffectiveRightsRLSDeclaredCollected: RLS без строки права не бывает, и
// под effective права всех слоёв прочитаны, поэтому отсутствие RLS честно
// называется complete_empty (declareCollected, ADR-030). raw заявления не
// делает: RLS роли расширения на заимствование он не видит, и его пустота
// ничего не доказывает.
func TestEffectiveRightsRLSDeclaredCollected(t *testing.T) {
	st := openFixtureStore(t)
	err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		seedEffComponents(t, tx)
		oid := seedEffObject(t, tx, "cfg", "Document", "Заметка")
		seedEffObject(t, tx, effExt, "Document", "Заметка")
		decl, _, err := tx.SourceFileID("cfg", declPath("Document", "Заметка"))
		if err != nil {
			return err
		}
		rid, err := tx.EnsureRole(store.Role{ComponentID: "cfg", NameNorm: "читатель", NameDisplay: "Читатель", FileID: decl, Layer: "base"})
		if err != nil {
			return err
		}
		return tx.InsertRoleRight(store.RoleRight{
			RoleID: rid, ObjectID: oid, ObjectNameNorm: "document.заметка", RightName: "Чтение", Value: true, OriginFileID: decl,
		})
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	task := "Пользователь не видит документ Заметка, нужен разбор прав и RLS"
	raw := buildFor(t, st, Request{Task: task, ProjectID: "p"})
	requireCoverageStatus(t, raw, "rls", Missing)
	eff := buildFor(t, st, Request{Task: task, ProjectID: "p", View: "effective"})
	requireCoverageStatus(t, eff, "rls", CompleteEmpty)
	requireCoverageStatus(t, eff, "profiles", Missing)
}
