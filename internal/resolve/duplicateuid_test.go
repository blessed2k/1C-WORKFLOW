package resolve

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
)

// TestResolveDuplicateUIDInOneModule — регрессия на реальной выгрузке
// отраслевой конфигурации: одно имя, объявленное дважды в одном модуле под
// взаимоисключающими ветками #Если/#Иначе, обязано резолвиться в ОДИН символ,
// а не в ambiguous с двумя кандидатами.
//
// Почему это не косметика: uid не включает span (§14), поэтому оба объявления
// делят один uid; publish оставляет первое (index_duplicate_symbol_uid), и два
// кандидата ambiguous легли бы двумя строками с одинаковой парой
// (ref_id, target_node_id) — UNIQUE constraint reference_candidate, вся
// индексация базы падала целиком (CommonForms/
// РедакторЭтикетокФормаРедактированияМакета, функция СохранитьXMLФайл).
func TestResolveDuplicateUIDInOneModule(t *testing.T) {
	const (
		formPath   = "CommonForms/Редактор/Ext/Form/Module.bsl"
		commonPath = "CommonModules/Помощник/Ext/Module.bsl"
		mgrPath    = "Catalogs/Товары/Ext/ManagerModule.bsl"
		callerPath = "CommonModules/Вызывающий/Ext/Module.bsl"
	)

	// Дубль имени внутри модуля: два объявления, один uid.
	formDup := []domain.Symbol{
		proc(formPath, "СохранитьXMLФайл", false),
		proc(formPath, "СохранитьXMLФайл", false),
	}
	commonDup := []domain.Symbol{
		proc(commonPath, "Обработать", true),
		proc(commonPath, "Обработать", true),
	}
	mgrDup := []domain.Symbol{
		proc(mgrPath, "НайтиПоКоду", true),
		proc(mgrPath, "НайтиПоКоду", true),
	}

	caller := ModuleEntry{ModulePath: callerPath, Kind: bsl.ModuleCommon, NameNorm: "вызывающий"}
	managerModule := ModuleEntry{
		ModulePath: mgrPath, Kind: bsl.ModuleManager,
		OwnerMType: "Catalog", OwnerNameNorm: domain.NormalizeName("Товары"),
		Layer: domain.BaseLayer(testComponent), Symbols: mgrDup,
	}

	tests := []struct {
		name    string
		modules []ModuleEntry
		raw     RawRef
		wantUID domain.SymbolUID
	}{
		{
			name: "местный модуль формы",
			modules: []ModuleEntry{{
				ModulePath: formPath, Kind: bsl.ModuleForm,
				Layer: domain.BaseLayer(testComponent), Symbols: formDup,
			}},
			raw: RawRef{
				Form:     FormUnqualified,
				NameNorm: domain.NormalizeName("СохранитьXMLФайл"), NameDisplay: "СохранитьXMLФайл",
				CallerModulePath: formPath, CallerModuleKind: bsl.ModuleForm,
			},
			wantUID: formDup[0].UID,
		},
		{
			name: "общий модуль по квалификатору",
			modules: []ModuleEntry{
				commonModule(commonPath, "Помощник", meta.ModuleRegistryFact{Server: true}, commonDup...),
				caller,
			},
			raw: RawRef{
				Form: FormQualifiedModule, QualifierNorm: domain.NormalizeName("Помощник"),
				NameNorm: domain.NormalizeName("Обработать"), NameDisplay: "Обработать",
				CallerModulePath: callerPath, CallerModuleKind: bsl.ModuleCommon,
			},
			wantUID: commonDup[0].UID,
		},
		{
			name:    "менеджерный модуль",
			modules: []ModuleEntry{managerModule, caller},
			raw: RawRef{
				Form:         FormQualifiedManager,
				ManagerMType: "Catalog", ManagerObjectNameNorm: domain.NormalizeName("Товары"),
				NameNorm: domain.NormalizeName("НайтиПоКоду"), NameDisplay: "НайтиПоКоду",
				CallerModulePath: callerPath, CallerModuleKind: bsl.ModuleCommon,
			},
			wantUID: mgrDup[0].UID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := mustEnv(t, EnvInput{Component: testComponent, Modules: tt.modules}, nil)
			res := Resolve(tt.raw, env)

			if res.Resolution != domain.ResolutionResolved {
				t.Fatalf("resolution = %s, ожидалось resolved (кандидаты: %+v)", res.Resolution, res.Candidates)
			}
			if len(res.Candidates) != 0 {
				t.Errorf("candidates = %d, у resolved кандидатов быть не должно: %+v", len(res.Candidates), res.Candidates)
			}
			if res.TargetUID != tt.wantUID {
				t.Errorf("targetUID = %s, ожидался %s (первое объявление по тексту)", res.TargetUID, tt.wantUID)
			}
		})
	}
}

// TestDedupeByUIDKeepsFirst — сам хелпер: порядок сохраняется, побеждает
// первое объявление (тем же правилом, что и publishModuleSymbols).
func TestDedupeByUIDKeepsFirst(t *testing.T) {
	const path = "CommonModules/М/Ext/Module.bsl"
	a := proc(path, "Альфа", true)
	b := proc(path, "Бета", true)

	tests := []struct {
		name string
		in   []domain.Symbol
		want []domain.SymbolUID
	}{
		{name: "пусто", in: nil, want: nil},
		{name: "один", in: []domain.Symbol{a}, want: []domain.SymbolUID{a.UID}},
		{name: "дубль схлопывается", in: []domain.Symbol{a, a}, want: []domain.SymbolUID{a.UID}},
		{name: "разные имена остаются", in: []domain.Symbol{a, b}, want: []domain.SymbolUID{a.UID, b.UID}},
		{name: "порядок сохраняется", in: []domain.Symbol{b, a, b}, want: []domain.SymbolUID{b.UID, a.UID}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedupeByUID(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("длина = %d, ожидалась %d", len(got), len(tt.want))
			}
			for i, uid := range tt.want {
				if got[i].UID != uid {
					t.Errorf("[%d] uid = %s, ожидался %s", i, got[i].UID, uid)
				}
			}
		})
	}
}
