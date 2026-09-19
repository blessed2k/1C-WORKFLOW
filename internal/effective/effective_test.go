package effective

import (
	"errors"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

const (
	testBase   = "base"
	testModule = "Documents/Док/Ext/ObjectModule.bsl"
)

func extComp(id, appliesTo string, order int) Extension {
	return Extension{ID: id, AppliesTo: appliesTo, ApplyOrder: order}
}

const moduleThreeKinds = `&Перед("ПередЗаписью")
Процедура РасшБ_ПередЗаписью(Отказ)
КонецПроцедуры

&После("ПриЗаписи")
Процедура РасшБ_ПриЗаписи()
КонецПроцедуры

&Вместо("ОбработкаПроведения")
Процедура РасшБ_ОбработкаПроведения(Отказ, РежимПроведения)
	ПродолжитьВызов(Отказ, РежимПроведения);
КонецПроцедуры
`

const moduleInstead = `&Вместо("ОбработкаПроведения")
Процедура Др_ОбработкаПроведения(Отказ, РежимПроведения)
КонецПроцедуры
`

// Таблица сценариев спеки C1. Каждый сценарий проверяет модуль только через
// in-memory источник: store здесь не нужен, и именно ради этого шов заведён.
func TestModule(t *testing.T) {
	mod := func(ext string) ModuleKey { return ModuleKey{Component: ext, Path: testModule} }
	tests := []struct {
		name  string
		src   *Memory
		check func(t *testing.T, r Result)
	}{
		{
			name: "нет расширений",
			src:  &Memory{Extensions: []Extension{extComp("other", "anotherBase", 1)}},
			check: func(t *testing.T, r Result) {
				if len(r.Intercepts) != 0 || len(r.BorrowedBy) != 0 || len(r.Diagnostics) != 0 {
					t.Fatalf("ожидался пустой результат, получено %+v", r)
				}
			},
		},
		{
			name: "расширение не заимствовало модуль",
			src: &Memory{
				Extensions: []Extension{extComp("ext", testBase, 1)},
				Modules:    map[ModuleKey][]byte{{Component: "ext", Path: "CommonModules/Другой/Ext/Module.bsl"}: []byte(moduleThreeKinds)},
			},
			check: func(t *testing.T, r Result) {
				if len(r.Intercepts) != 0 || len(r.BorrowedBy) != 0 {
					t.Fatalf("модуль не заимствован, получено %+v", r)
				}
			},
		},
		{
			name: "заимствовало без перехватчиков",
			src: &Memory{
				Extensions: []Extension{extComp("ext", testBase, 1)},
				Modules:    map[ModuleKey][]byte{mod("ext"): []byte("Процедура Своя()\nКонецПроцедуры\n")},
			},
			check: func(t *testing.T, r Result) {
				if len(r.Intercepts) != 0 {
					t.Fatalf("перехватчиков быть не должно: %+v", r.Intercepts)
				}
				if len(r.BorrowedBy) != 1 || r.BorrowedBy[0] != "ext" {
					t.Fatalf("BorrowedBy = %v, want [ext]", r.BorrowedBy)
				}
			},
		},
		{
			name: "Перед, После и Вместо с текстом",
			src: &Memory{
				Extensions: []Extension{extComp("ext", testBase, 1)},
				Modules:    map[ModuleKey][]byte{mod("ext"): []byte(moduleThreeKinds)},
			},
			check: func(t *testing.T, r Result) {
				if len(r.Intercepts) != 3 {
					t.Fatalf("Intercepts = %d, want 3: %+v", len(r.Intercepts), r.Intercepts)
				}
				kinds := []resolve.InterceptKind{resolve.InterceptBefore, resolve.InterceptAfter, resolve.InterceptInstead}
				for i, ic := range r.Intercepts {
					if ic.Kind != kinds[i] {
						t.Errorf("Intercepts[%d].Kind = %s, want %s", i, ic.Kind, kinds[i])
					}
					if ic.Layer.Component != "ext" || ic.Layer.ApplyOrder != 1 {
						t.Errorf("Intercepts[%d].Layer = %+v", i, ic.Layer)
					}
					if !strings.HasPrefix(ic.Text, "&") && !strings.HasPrefix(ic.Text, "Процедура") {
						t.Errorf("Intercepts[%d].Text = %q: не текст перехватчика", i, ic.Text)
					}
				}
				if !strings.Contains(r.Intercepts[2].Text, "ПродолжитьВызов") {
					t.Errorf("текст &Вместо не взят из blob: %q", r.Intercepts[2].Text)
				}
			},
		},
		{
			name: "два расширения в порядке applyOrder, не в порядке входа",
			src: &Memory{
				Extensions: []Extension{
					extComp("zeta", testBase, 2),
					extComp("alpha", testBase, 1),
					extComp("other", "anotherBase", 0),
				},
				Modules: map[ModuleKey][]byte{
					mod("zeta"):  []byte(moduleInstead),
					mod("alpha"): []byte(moduleInstead),
					mod("other"): []byte(moduleInstead),
				},
			},
			check: func(t *testing.T, r Result) {
				got := []string{}
				for _, ic := range r.Intercepts {
					got = append(got, string(ic.Layer.Component))
				}
				if strings.Join(got, ",") != "alpha,zeta" {
					t.Fatalf("порядок перехватчиков = %v, want [alpha zeta]", got)
				}
				if strings.Join(r.BorrowedBy, ",") != "alpha,zeta" {
					t.Fatalf("BorrowedBy = %v, want [alpha zeta]", r.BorrowedBy)
				}
			},
		},
		{
			name: "blob недоступен: диагностика, остальные расширения обработаны",
			src: &Memory{
				Extensions: []Extension{extComp("broken", testBase, 1), extComp("ok", testBase, 2)},
				Modules:    map[ModuleKey][]byte{mod("ok"): []byte(moduleInstead)},
				Unreadable: map[ModuleKey]error{mod("broken"): errors.New("blob вычищен")},
			},
			check: func(t *testing.T, r Result) {
				if len(r.Diagnostics) != 1 {
					t.Fatalf("Diagnostics = %+v, want одну", r.Diagnostics)
				}
				d := r.Diagnostics[0]
				if d.Code != codeBlobUnavailable || d.Hint == "" ||
					!strings.Contains(d.Message, "broken") || !strings.Contains(d.Message, "blob вычищен") {
					t.Fatalf("диагностика = %+v", d)
				}
				// Нечитаемый исходник не отменяет факта заимствования.
				if strings.Join(r.BorrowedBy, ",") != "broken,ok" {
					t.Fatalf("BorrowedBy = %v, want [broken ok]", r.BorrowedBy)
				}
				if len(r.Intercepts) != 1 || r.Intercepts[0].Layer.Component != "ok" {
					t.Fatalf("второе расширение не обработано: %+v", r.Intercepts)
				}
			},
		},
		{
			name: "аннотация без цели: диагностика ADR-027",
			src: &Memory{
				Extensions: []Extension{extComp("ext", testBase, 1)},
				Modules:    map[ModuleKey][]byte{mod("ext"): []byte("&Вместо\nПроцедура РасшБ_ОбработкаПроведения()\nКонецПроцедуры\n")},
			},
			check: func(t *testing.T, r Result) {
				if len(r.Intercepts) != 0 {
					t.Fatalf("факт без цели строиться не должен: %+v", r.Intercepts)
				}
				if len(r.Diagnostics) != 1 || r.Diagnostics[0].Code != resolve.DiagInterceptTargetUnknown ||
					!strings.Contains(r.Diagnostics[0].Message, "расширение ext") {
					t.Fatalf("Diagnostics = %+v", r.Diagnostics)
				}
				if !strings.Contains(r.Diagnostics[0].Hint, "get_module_structure component=ext view=effective") {
					t.Fatalf("подсказка без адреса исходника: %q", r.Diagnostics[0].Hint)
				}
			},
		},
		{
			name: "равный applyOrder: порядок по id компонента",
			src: &Memory{
				Extensions: []Extension{extComp("beta", testBase, 1), extComp("alpha", testBase, 1)},
				Modules: map[ModuleKey][]byte{
					mod("beta"):  []byte(moduleInstead),
					mod("alpha"): []byte(moduleInstead),
				},
			},
			check: func(t *testing.T, r Result) {
				if strings.Join(r.BorrowedBy, ",") != "alpha,beta" {
					t.Fatalf("BorrowedBy = %v, want [alpha beta]", r.BorrowedBy)
				}
			},
		},
		{
			name: "заимствовано, но строки файла нет: без перехватчиков и без диагностики",
			src: &Memory{
				Extensions: []Extension{extComp("ext", testBase, 1)},
				NoFileRow:  map[ModuleKey]bool{mod("ext"): true},
			},
			check: func(t *testing.T, r Result) {
				if len(r.Intercepts) != 0 || len(r.Diagnostics) != 0 {
					t.Fatalf("ожидались пустые перехватчики и диагностики: %+v", r)
				}
				if len(r.BorrowedBy) != 1 || r.BorrowedBy[0] != "ext" {
					t.Fatalf("BorrowedBy = %v, want [ext]", r.BorrowedBy)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Module(tt.src, testBase, testModule)
			if err != nil {
				t.Fatalf("Module: %v", err)
			}
			tt.check(t, r)
		})
	}
}

// Отказ источника, не являющийся недоступностью blob, прерывает вычисление:
// «не смогли прочитать индекс» не имеет права выглядеть как «перехватчиков нет».
func TestModuleHardErrorAborts(t *testing.T) {
	src := &Memory{
		Extensions: []Extension{extComp("ext", testBase, 1)},
		Failing:    map[ModuleKey]error{{Component: "ext", Path: testModule}: errors.New("source_file недоступен")},
	}
	if _, err := Module(src, testBase, testModule); err == nil {
		t.Fatal("ожидалась ошибка")
	}
}

func TestInsteadConflict(t *testing.T) {
	src := &Memory{
		Extensions: []Extension{extComp("alpha", testBase, 1), extComp("zeta", testBase, 2)},
		Modules: map[ModuleKey][]byte{
			{Component: "alpha", Path: testModule}: []byte(moduleInstead),
			{Component: "zeta", Path: testModule}:  []byte(moduleInstead),
		},
	}
	r, err := Module(src, testBase, testModule)
	if err != nil {
		t.Fatalf("Module: %v", err)
	}
	ics := make([]resolve.Intercept, len(r.Intercepts))
	for i, ic := range r.Intercepts {
		ics[i] = ic.Intercept
	}
	conflicts := resolve.DetectInsteadConflicts(ics)
	if len(conflicts) != 1 {
		t.Fatalf("конфликтов = %d, want 1", len(conflicts))
	}
	txt := InsteadConflict(conflicts[0])
	if txt.Code != codeInsteadConflict || txt.Hint == "" ||
		!strings.Contains(txt.Message, "alpha, zeta") || !strings.Contains(txt.Message, "confidence=") {
		t.Fatalf("текст конфликта = %+v", txt)
	}
}

// Вид компонента отсекается при переводе из store: базовая конфигурация с тем
// же AppliesTo расширением не становится.
func TestExtensionsFromStore(t *testing.T) {
	got := ExtensionsFromStore([]store.Component{
		{ID: "cfg", Kind: string(domain.KindConfiguration)},
		{ID: "ext", Kind: string(domain.KindExtension), AppliesTo: "cfg", ApplyOrder: 3},
	})
	if len(got) != 1 || got[0] != (Extension{ID: "ext", AppliesTo: "cfg", ApplyOrder: 3}) {
		t.Fatalf("ExtensionsFromStore = %+v", got)
	}
}

type fakeObjectSource struct {
	comps   []store.Component
	rows    []store.MetadataObjectRow
	compErr error
	rowsErr error
}

func (f fakeObjectSource) Components() ([]store.Component, error) { return f.comps, f.compErr }

func (f fakeObjectSource) MetadataObjectsByName(mtype, nameNorm string) ([]store.MetadataObjectRow, error) {
	if f.rowsErr != nil {
		return nil, f.rowsErr
	}
	var out []store.MetadataObjectRow
	for _, r := range f.rows {
		if r.MType == mtype && r.NameNorm == nameNorm {
			out = append(out, r)
		}
	}
	return out, nil
}

// TestBorrowedObjects: в effective-вид объекта входят строки ТОЛЬКО тех
// расширений, что применяются к его компоненту, в порядке наложения; строка
// самого объекта, чужая база и чужой вид объекта не входят.
func TestBorrowedObjects(t *testing.T) {
	obj := store.MetadataObjectRow{ID: 1, ComponentID: "cfg", MType: "Document", NameNorm: "заказ", NameDisplay: "Заказ"}
	src := fakeObjectSource{
		comps: []store.Component{
			{ID: "cfg", Kind: string(domain.KindConfiguration)},
			{ID: "ext-b", Kind: string(domain.KindExtension), AppliesTo: "cfg", ApplyOrder: 2},
			{ID: "ext-a", Kind: string(domain.KindExtension), AppliesTo: "cfg", ApplyOrder: 1},
			{ID: "ext-other", Kind: string(domain.KindExtension), AppliesTo: "cfg2", ApplyOrder: 1},
		},
		rows: []store.MetadataObjectRow{
			obj,
			{ID: 2, ComponentID: "ext-b", MType: "Document", NameNorm: "заказ"},
			{ID: 3, ComponentID: "ext-a", MType: "Document", NameNorm: "заказ"},
			{ID: 4, ComponentID: "ext-other", MType: "Document", NameNorm: "заказ"},
			{ID: 5, ComponentID: "ext-a", MType: "Catalog", NameNorm: "заказ"},
		},
	}
	got, err := BorrowedObjects(src, obj)
	if err != nil {
		t.Fatalf("BorrowedObjects: %v", err)
	}
	var ids []int64
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if len(ids) != 2 || ids[0] != 3 || ids[1] != 2 {
		t.Fatalf("BorrowedObjects = %v, want [3 2] (ext-a, затем ext-b; без самой строки, чужой базы и чужого вида)", ids)
	}

	t.Run("отказ чтения не выглядит пустотой", func(t *testing.T) {
		for _, bad := range []fakeObjectSource{
			{compErr: errors.New("boom")},
			{comps: src.comps, rowsErr: errors.New("boom")},
		} {
			if _, err := BorrowedObjects(bad, obj); err == nil {
				t.Fatal("ожидалась ошибка чтения, получена тишина")
			}
		}
	})
}
