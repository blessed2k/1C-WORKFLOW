package resolve

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
)

// TestDeriveRegisterAccessModes — критерий приёмки: register_access
// различает read/write/movement/clear, и там, где регистр опознан по имени
// (Env.Objects), объект разрешён.
func TestDeriveRegisterAccessModes(t *testing.T) {
	src := []byte("Процедура Тест() Экспорт\n" +
		"\tОстатки = РегистрыНакопления.ОстаткиТоваров.Получить();\n" +
		"\tНабор = РегистрыНакопления.ОстаткиТоваров.СоздатьНаборЗаписей();\n" +
		"\tНабор.Записать();\n" +
		"\tНабор.Очистить();\n" +
		"\tДвижения.ОстаткиТоваров.Записать();\n" +
		"КонецПроцедуры\n")
	mod, diags := bsl.Parse(src, bsl.Options{File: "Documents/Реализация/Ext/ObjectModule.bsl"})
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %v", diags)
	}
	if len(mod.RegisterAccesses) != 4 {
		t.Fatalf("register accesses = %d, ожидалось 4: %+v", len(mod.RegisterAccesses), mod.RegisterAccesses)
	}

	env := mustEnv(t, EnvInput{
		Component: testComponent,
		Objects: []ObjectRef{
			{MType: "AccumulationRegister", NameNorm: "остаткитоваров", IdentityKey: "metadata:cfg:AccumulationRegister:остаткитоваров"},
		},
	}, nil)

	results := DeriveRegisterAccess(mod, env)
	if len(results) != 4 {
		t.Fatalf("results = %d, ожидалось 4", len(results))
	}

	modes := map[bsl.RegisterMode]int{}
	for _, r := range results {
		modes[r.Mode]++
		if !r.ObjectResolved {
			t.Errorf("объект не разрешён для %+v", r)
		}
	}
	want := map[bsl.RegisterMode]int{
		bsl.ModeRead:     1,
		bsl.ModeWrite:    1,
		bsl.ModeClear:    1,
		bsl.ModeMovement: 1,
	}
	for mode, n := range want {
		if modes[mode] != n {
			t.Errorf("mode %s: получено %d, ожидалось %d (все режимы: %+v)", mode, modes[mode], n, modes)
		}
	}
}

// TestDeriveRegisterAccessUnknownObject — регистр не найден в Env.Objects:
// доступ остаётся в выдаче с ObjectResolved=false, а не теряется.
func TestDeriveRegisterAccessUnknownObject(t *testing.T) {
	src := []byte("Процедура Тест() Экспорт\n" +
		"\tОстатки = РегистрыСведений.Неизвестный.Получить();\n" +
		"КонецПроцедуры\n")
	mod, diags := bsl.Parse(src, bsl.Options{File: "CommonModules/X/Ext/Module.bsl"})
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %v", diags)
	}
	env := mustEnv(t, EnvInput{Component: testComponent}, nil)
	results := DeriveRegisterAccess(mod, env)
	if len(results) != 1 {
		t.Fatalf("results = %d, ожидался 1", len(results))
	}
	if results[0].ObjectResolved {
		t.Error("объект не должен был разрешиться")
	}
	if results[0].Mode != bsl.ModeRead {
		t.Errorf("mode = %s, ожидалось read", results[0].Mode)
	}
}
