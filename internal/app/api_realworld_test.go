package app

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// TestRealDumpFindAPI: find_api на реальной выгрузке (написан под ut_demo, как
// и остальные real-dump тесты пакета). Ожидаемые методы выписаны из самой
// выгрузки руками, а не получены от ранжировщика: это методы БСП, которые
// разработчик назвал бы ответом на запрос.
func TestRealDumpFindAPI(t *testing.T) {
	p, _ := newRealDumpProject(t)
	svc := NewAPIService(p)
	ctx := context.Background()

	find := func(t *testing.T, query string, limit int) APISearchItem {
		t.Helper()
		t0 := time.Now()
		resp, err := svc.FindAPI(ctx, FindAPIInput{Query: query, Limit: limit})
		if err != nil {
			t.Fatalf("FindAPI(%q): %v", query, err)
		}
		item := resp.Items[0]
		t.Logf("%q: %s, bsp %d из %d, other %d из %d", query, time.Since(t0).Round(time.Millisecond),
			len(item.BSP), item.BSPMatched, len(item.Other), item.OtherMatched)
		for _, w := range resp.Warnings {
			t.Errorf("%q: предупреждение %s: %s", query, w.Code, w.Message)
		}
		return item
	}

	const top = 3
	inTop := func(items []APIMethodItem, want []string) bool {
		for i, it := range items {
			if i >= top {
				break
			}
			if slices.Contains(want, it.Call) {
				return true
			}
		}
		return false
	}

	t.Run("методы БСП в первой тройке секции bsp", func(t *testing.T) {
		for _, c := range []struct {
			query string
			want  []string
		}{
			{"значение реквизита объекта по ссылке", []string{"ОбщегоНазначения.ЗначениеРеквизитаОбъекта"}},
			{"выполнить функцию в несколько потоков в фоновых заданиях", []string{"ДлительныеОперации.ВыполнитьФункциюВНесколькоПотоков"}},
			{"разбить строку по разделителю", []string{"СтроковыеФункцииКлиентСервер.РазложитьСтрокуВМассивПодстрок"}},
			{"скачать файл из интернета", []string{"ПолучениеФайловИзИнтернета.СкачатьФайлНаСервере", "ПолучениеФайловИзИнтернета.СкачатьФайлВоВременноеХранилище"}},
			{"сумма прописью", []string{"РаботаСКурсамиВалют.СформироватьСуммуПрописью"}},
			{"проверить что ссылка существует в базе", []string{"ОбщегоНазначения.СсылкаСуществует"}},
			{"удалить недопустимые символы из имени файла", []string{"ОбщегоНазначенияКлиентСервер.ЗаменитьНедопустимыеСимволыВИмениФайла"}},
		} {
			item := find(t, c.query, 0)
			if !inTop(item.BSP, c.want) {
				t.Errorf("%q: в первой тройке bsp нет %q, есть %q", c.query, c.want, apiCalls(item.BSP))
			}
			for _, it := range item.BSP {
				if apiOverridableModule(strings.SplitN(it.Call, ".", 2)[0]) {
					t.Errorf("%q: в bsp метод переопределяемого модуля %s", c.query, it.Call)
				}
			}
		}
	})

	t.Run("прикладной метод и метод менеджера в секции other", func(t *testing.T) {
		item := find(t, "пересчитать сумму документа в валюту", 0)
		if !inTop(item.Other, []string{"РаботаСКурсамиВалютУТ.ПересчитатьСуммуДокументаВВалюту"}) {
			t.Errorf("в первой тройке other нет РаботаСКурсамиВалютУТ.ПересчитатьСуммуДокументаВВалюту, есть %q", apiCalls(item.Other))
		}
		item = find(t, "получить реквизиты для ключа цен", 0)
		if !inTop(item.Other, []string{"Справочники.ВидыНоменклатуры.ПолучитьРеквизитыДляКлючаЦен"}) {
			t.Errorf("в первой тройке other нет Справочники.ВидыНоменклатуры.ПолучитьРеквизитыДляКлючаЦен, есть %q", apiCalls(item.Other))
		}
	})

	t.Run("устаревший метод помечен и называет замену", func(t *testing.T) {
		item := find(t, "идентификатор рабочего стола", maxAPILimit)
		i := slices.IndexFunc(item.BSP, func(it APIMethodItem) bool {
			return it.Call == "ДополнительныеОтчетыИОбработкиКлиентСервер.ИдентификаторРабочегоСтола"
		})
		if i < 0 {
			t.Fatalf("устаревший метод не найден в bsp: %q", apiCalls(item.BSP))
		}
		if got := item.BSP[i]; !got.Deprecated || !strings.Contains(got.Summary, "Следует использовать") {
			t.Errorf("устаревший метод: deprecated = %v, summary = %q", got.Deprecated, got.Summary)
		}
	})

	t.Run("замер частей вызова", func(t *testing.T) {
		op, err := p.Active(ctx)
		if err != nil {
			t.Fatalf("Active: %v", err)
		}
		type parts struct {
			rows, objects    int
			methods, library time.Duration
		}
		got, err := readTx(ctx, op.Store, func(tx *store.ReadTx) (parts, error) {
			var out parts
			t0 := time.Now()
			rows, err := tx.ExportedMethodsInRegions(apiModuleKinds, apiTopRegions)
			if err != nil {
				return out, err
			}
			out.rows, out.methods = len(rows), time.Since(t0)
			t0 = time.Now()
			lib, err := readAPILibrary(tx)
			if err != nil {
				return out, err
			}
			out.objects, out.library = len(lib.objects), time.Since(t0)
			return out, nil
		})
		if err != nil {
			t.Fatalf("readTx: %v", err)
		}
		t.Logf("методов программного интерфейса %d за %s; состав библиотеки %d объектов за %s",
			got.rows, got.methods.Round(time.Millisecond), got.objects, got.library.Round(time.Millisecond))
		if got.rows == 0 || got.objects == 0 {
			t.Errorf("пустой замер: методов %d, объектов библиотеки %d", got.rows, got.objects)
		}
	})

	t.Run("версия библиотеки прочитана из выгрузки", func(t *testing.T) {
		item := find(t, "значение реквизита объекта", 0)
		if !regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`).MatchString(item.BSPVersion) {
			t.Errorf("BSPVersion = %q, want вида 3.1.11.366", item.BSPVersion)
		}
		t.Logf("версия БСП: %s", item.BSPVersion)
	})
}
