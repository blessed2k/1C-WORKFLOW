package main

import (
	"fmt"
	"reflect"
	"testing"
)

func testMethods(n int) []method {
	out := make([]method, n)
	for i := range out {
		out[i] = method{Section: sectionBSP, Call: fmt.Sprintf("Модуль.Метод%02d", i), UID: fmt.Sprintf("uid-%02d", i)}
		// Метод i зовут из i мест; нулевой не зовут ниоткуда.
		for s := 0; s < i; s++ {
			out[i].Sites = append(out[i].Sites, callSite{Module: fmt.Sprintf("М%d", s), Line: s + 1})
		}
	}
	return out
}

func pickedCalls(ps []picked, stratum string) []string {
	var out []string
	for _, p := range ps {
		if p.Stratum == stratum {
			out = append(out, p.Call)
		}
	}
	return out
}

// TestPickLibrary: в ядро идут самые вызываемые, остальное добирается
// случайно без повторов, методы без вызовов не берутся; выборка одинакова от
// запуска к запуску и не зависит от порядка входа.
func TestPickLibrary(t *testing.T) {
	methods := testMethods(20)
	got := pickLibrary(methods, 3, 5, 1, map[string]bool{})

	if want := []string{"Модуль.Метод19", "Модуль.Метод18", "Модуль.Метод17"}; !reflect.DeepEqual(pickedCalls(got, stratumCore), want) {
		t.Errorf("ядро = %q, want %q", pickedCalls(got, stratumCore), want)
	}
	rest := pickedCalls(got, stratumRest)
	if len(rest) != 5 {
		t.Fatalf("остальных %d, want 5: %q", len(rest), rest)
	}
	seen := map[string]bool{}
	for _, p := range got {
		if seen[p.Call] {
			t.Errorf("метод %s взят дважды", p.Call)
		}
		seen[p.Call] = true
		if p.Call == "Модуль.Метод00" {
			t.Errorf("взят метод без вызовов")
		}
	}

	reversed := append([]method(nil), methods...)
	for l, r := 0, len(reversed)-1; l < r; l, r = l+1, r-1 {
		reversed[l], reversed[r] = reversed[r], reversed[l]
	}
	if again := pickLibrary(reversed, 3, 5, 1, map[string]bool{}); !reflect.DeepEqual(again, got) {
		t.Errorf("выборка зависит от порядка входа:\n %v\n %v", pickedCalls(again, ""), pickedCalls(got, ""))
	}
	if other := pickedCalls(pickLibrary(methods, 3, 5, 2, map[string]bool{}), stratumRest); reflect.DeepEqual(other, rest) {
		t.Errorf("другое зерно дало ту же случайную часть: %q", other)
	}
	if all := pickLibrary(methods, 100, 100, 1, map[string]bool{}); len(all) != 19 {
		t.Errorf("при избыточных квотах взято %d, want 19 (все вызываемые)", len(all))
	}
}

// TestPickLibraryOneMethodPerFamily: из метода и его близнеца по месту
// исполнения в набор идёт один, и семейство, уже взятое раньше, не берётся.
func TestPickLibraryOneMethodPerFamily(t *testing.T) {
	site := []callSite{{Module: "М", Line: 1}}
	methods := []method{
		{Call: "ОбщегоНазначения.Сообщить", UID: "a", Sites: append(site, site...)},
		{Call: "ОбщегоНазначенияКлиент.Сообщить", UID: "b", Sites: site},
		{Call: "ОбщегоНазначения.Другой", UID: "c", Sites: site},
		{Call: "Пользователи.Текущий", UID: "d", Sites: site},
	}
	taken := map[string]bool{callFamily("ПользователиКлиент.Текущий"): true}
	got := pickLibrary(methods, 1, 10, 1, taken)
	var calls []string
	for _, p := range got {
		calls = append(calls, p.Call)
	}
	if len(got) != 2 || got[0].Call != "ОбщегоНазначения.Сообщить" || got[1].Call != "ОбщегоНазначения.Другой" {
		t.Errorf("взяты %q, want ОбщегоНазначения.Сообщить и ОбщегоНазначения.Другой", calls)
	}
	if !taken[callFamily("ОбщегоНазначения.Другой")] {
		t.Errorf("взятое семейство не записано в taken")
	}
}

// TestAmbiguousNames: имя метода, которое носят три семейства и больше,
// неоднозначно; близнецы одного семейства за разные семейства не считаются.
func TestAmbiguousNames(t *testing.T) {
	got := ambiguousNames([]string{
		"Документы.Заказ.ЗаполнитьИмена", "Документы.Счет.ЗаполнитьИмена", "Документы.Акт.заполнитьимена",
		"ОбщегоНазначения.Сообщить", "ОбщегоНазначенияКлиент.Сообщить", "ОбщегоНазначенияКлиентСервер.Сообщить",
		"Продажи.Пересчитать", "Закупки.Пересчитать",
	})
	if !got["заполнитьимена"] || got["сообщить"] || got["пересчитать"] || len(got) != 1 {
		t.Errorf("ambiguousNames = %v, want только заполнитьимена", got)
	}
}

// TestSiteOrder: порядок перебора мест вызова не зависит от порядка, в
// котором они пришли, и обходит все места по одному разу.
func TestSiteOrder(t *testing.T) {
	m := testMethods(8)[7]
	want := siteOrder(m, 1)
	m.Sites[0], m.Sites[6] = m.Sites[6], m.Sites[0]
	got := siteOrder(m, 1)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("siteOrder = %+v, want %+v", got, want)
	}
	seen := map[callSite]bool{}
	for _, s := range got {
		seen[s] = true
	}
	if len(got) != 7 || len(seen) != 7 {
		t.Errorf("мест в порядке перебора %d, разных %d, want 7", len(got), len(seen))
	}
	if siteOrder(method{UID: "без-вызовов"}, 1) != nil {
		t.Errorf("у метода без вызовов порядок не пуст")
	}
}

// TestCallFamily: близнецы модуля по месту исполнения сводятся в одно
// семейство, разные модули и вызовы через менеджер объекта нет.
func TestCallFamily(t *testing.T) {
	same := [][2]string{
		{"ОбщегоНазначения.СообщитьПользователю", "ОбщегоНазначенияКлиент.СообщитьПользователю"},
		{"ОбщегоНазначения.ЗначениеВМассиве", "ОбщегоНазначенияКлиентСервер.значениевмассиве"},
		{"Пользователи.ТекущийПользователь", "ПользователиКлиентСервер.ТекущийПользователь"},
		{"ФайлыВызовСервера.Данные", "ФайлыКлиент.Данные"},
		{"МодульПовтИсп.Значение", "Модуль.Значение"},
	}
	for _, p := range same {
		if callFamily(p[0]) != callFamily(p[1]) {
			t.Errorf("%s и %s в разных семействах: %q и %q", p[0], p[1], callFamily(p[0]), callFamily(p[1]))
		}
	}
	different := [][2]string{
		{"ОбщегоНазначения.СообщитьПользователю", "Пользователи.СообщитьПользователю"},
		{"ОбщегоНазначения.ЗначениеРеквизитаОбъекта", "ОбщегоНазначения.ЗначенияРеквизитовОбъекта"},
		{"Справочники.Клиент.Метод", "Справочники.КлиентСервер.Метод"},
		// Имя модуля из одного суффикса не обнуляется.
		{"Клиент.Метод", "Сервер.Метод"},
	}
	for _, p := range different {
		if callFamily(p[0]) == callFamily(p[1]) {
			t.Errorf("%s и %s в одном семействе %q", p[0], p[1], callFamily(p[0]))
		}
	}
}

// TestAcceptedCalls: сам метод первым, за ним близнецы по семейству и
// одноимённые методы другого семейства.
func TestAcceptedCalls(t *testing.T) {
	calls := []string{
		"ОбщегоНазначенияКлиент.СообщитьПользователю", "ОбщегоНазначения.СообщитьПользователю",
		"ОбщегоНазначения.Другой", "ПодборТоваров.СообщитьПользователю",
	}
	got := acceptedCalls("ОбщегоНазначенияКлиент.СообщитьПользователю", familyIndex(calls), nameIndex(calls))
	want := []string{
		"ОбщегоНазначенияКлиент.СообщитьПользователю", "ОбщегоНазначения.СообщитьПользователю",
		"ПодборТоваров.СообщитьПользователю",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("acceptedCalls = %q, want %q", got, want)
	}
}
