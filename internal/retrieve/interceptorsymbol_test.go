package retrieve

import (
	"context"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// Фикстура этих тестов — минимальный документ Заказ с перехватчиком
// расширения, у которого можно ЗАДАТЬ число символов в индексе: 1 (штатно),
// 0 (символ не найден) и 2 (неоднозначность). Отдельная от seedPostingFixture:
// та заводит ровно один символ перехватчика и другую вариативность не
// выражает.
const interceptorExtBody = "&Вместо(\"ОбработкаПроведения\")\n" +
	"Процедура РасшА_ОбработкаПроведения(Отказ, РежимПроведения)\n" +
	"\tПродолжитьВызов(Отказ, РежимПроведения);\n" +
	"\tДвижения.ДвиженияДсСотрудников.Записать();\n" +
	"КонецПроцедуры"

// seedInterceptorSymbolFixture строит Заказ + расширение ext-a,
// заимствующее модуль объекта. extSymbols — сколько символов перехватчика
// РасшА_ОбработкаПроведения лежит в индексе для этого модуля.
func seedInterceptorSymbolFixture(t *testing.T, st *store.Store, extSymbols int) {
	t.Helper()
	baseBody := "Процедура ОбработкаПроведения(Отказ, РежимПроведения)\n\tДвижения.ТоварыНаСкладах.Записать();\nКонецПроцедуры"

	err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		fReg := fileHelper(t, tx, "cfg", declPath("AccumulationRegister", "ДвиженияДсСотрудников"), "<meta/>")
		oReg, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00AccumulationRegister\x00движениядссотрудников", ComponentID: "cfg",
			MType: "AccumulationRegister", NameNorm: "движениядссотрудников", NameDisplay: "ДвиженияДсСотрудников",
			FileID: fReg, Layer: "base",
		})
		if err != nil {
			return err
		}
		fMeta := fileHelper(t, tx, "cfg", declPath("Document", "Заказ"), "<meta/>")
		if _, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00Document\x00заказ", ComponentID: "cfg",
			MType: "Document", NameNorm: "заказ", NameDisplay: "Заказ", FileID: fMeta, Layer: "base",
		}); err != nil {
			return err
		}
		fMod := fileHelper(t, tx, "cfg", postingModuleZakaz, baseBody)
		mMod := moduleHelper(t, tx, "cfg", postingModuleZakaz, "заказ", "Заказ", fMod)
		symbolHelper(t, tx, symbolSpec{
			uid: "sym-base-op", componentID: "cfg", nameNorm: "обработкапроведения",
			nameDisplay: "ОбработкаПроведения", kind: "procedure", moduleID: mMod, fileID: fMod,
			span: spanOf(0, len(baseBody)),
		})

		if err := tx.UpsertComponent(store.Component{
			ID: "ext-a", Kind: "extension", Root: "ext-a", AppliesTo: "cfg", ApplyOrder: 1,
		}); err != nil {
			return err
		}
		fExt := fileHelper(t, tx, "ext-a", postingModuleZakaz, interceptorExtBody)
		mExt := moduleHelper(t, tx, "ext-a", postingModuleZakaz, "заказ", "Заказ", fExt)
		for i := 0; i < extSymbols; i++ {
			sExt := symbolHelper(t, tx, symbolSpec{
				uid: "sym-ext-op-" + string(rune('a'+i)), componentID: "ext-a", nameNorm: "расша_обработкапроведения",
				nameDisplay: "РасшА_ОбработкаПроведения", kind: "procedure", moduleID: mExt, fileID: fExt,
				span: spanOf(0, len(interceptorExtBody)),
			})
			if err := tx.InsertRegisterAccess(store.RegisterAccess{
				FileID: fExt, SymbolID: sExt, ObjectID: oReg, RegisterNameNorm: "движениядссотрудников",
				Mode: "movement", Static: true, Confidence: 1, Span: sp(), Layer: "ext-a",
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seedInterceptorSymbolFixture: %v", err)
	}
}

func warningCodes(res Result) map[string]int {
	out := map[string]int{}
	for _, w := range res.Warnings {
		out[w.Code]++
	}
	return out
}

// TestInterceptorMovementsCarryDisplayName — пункт 3 таска 09: у движений
// перехватчика From несёт DISPLAY-имя ("РасшА_ОбработкаПроведения"), как и у
// базового обработчика ("ОбработкаПроведения"). До правки туда уходило
// НОРМАЛИЗОВАННОЕ имя из resolve.Intercept, и в одной категории ответа
// оказывались два разных регистра написания.
func TestInterceptorMovementsCarryDisplayName(t *testing.T) {
	st := openFixtureStore(t)
	seedInterceptorSymbolFixture(t, st, 1)

	res := buildFor(t, st, Request{Task: postingTask, ProjectID: "p", View: "effective"})
	var got []string
	for _, r := range res.Relations {
		if r.Kind == "register_access" && strings.Contains(r.To, "движениядссотрудников") {
			got = append(got, r.From)
		}
	}
	if len(got) != 1 {
		t.Fatalf("движений перехватчика = %d, want 1: %+v", len(got), res.Relations)
	}
	if got[0] != "РасшА_ОбработкаПроведения" {
		t.Errorf("From = %q, want %q (display-имя перехватчика)", got[0], "РасшА_ОбработкаПроведения")
	}
}

// TestInterceptorSymbolNotFoundWarns — пункт 2 таска 09: перехватчик в ответе
// есть, а его символа в индексе нет, поэтому движений у него нет. Молчаливый
// continue делал это неотличимым от «перехватчик ничего не пишет» —
// обязано быть названо предупреждением.
func TestInterceptorSymbolNotFoundWarns(t *testing.T) {
	st := openFixtureStore(t)
	seedInterceptorSymbolFixture(t, st, 0)

	res := buildFor(t, st, Request{Task: postingTask, ProjectID: "p", View: "effective"})
	codes := warningCodes(res)
	if codes["interceptor_symbol_not_found"] != 1 {
		t.Errorf("interceptor_symbol_not_found = %d, want 1: %+v", codes["interceptor_symbol_not_found"], res.Warnings)
	}
}

// TestInterceptorSymbolAmbiguousWarns — пункт 2 таска 09, вторая половина:
// имени перехватчика в одном модуле расширения отвечают два символа. Выбор
// первого совпадения молчком — тот же молчаливый выбор одного слоя, который
// уже запрещён для &Вместо (instead_conflict).
func TestInterceptorSymbolAmbiguousWarns(t *testing.T) {
	st := openFixtureStore(t)
	seedInterceptorSymbolFixture(t, st, 2)

	res := buildFor(t, st, Request{Task: postingTask, ProjectID: "p", View: "effective"})
	codes := warningCodes(res)
	if codes["interceptor_symbol_ambiguous"] != 1 {
		t.Errorf("interceptor_symbol_ambiguous = %d, want 1: %+v", codes["interceptor_symbol_ambiguous"], res.Warnings)
	}
}
