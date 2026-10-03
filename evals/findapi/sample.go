package main

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"strconv"
	"strings"
)

// Секции набора: те же, что у ответа find_api.
const (
	sectionBSP   = "bsp"
	sectionOther = "other"
)

// Слои выборки методов библиотеки.
const (
	// stratumCore: самые вызываемые методы. Их агенту нужно находить чаще
	// всего, и в случайной выборке из тысяч методов они почти не встречаются.
	stratumCore = "core"
	// stratumRest: случайная выборка из остальных. Это методы, которых агент
	// не помнит по имени, то есть ровно те, ради которых нужен поиск.
	stratumRest = "rest"
)

// callSite: место вызова метода в чужом модуле.
type callSite struct {
	Module string `json:"module"`
	Line   int    `json:"line"`
}

// method: метод программного интерфейса, каким он нужен выборке.
type method struct {
	Section   string
	Call      string
	UID       string
	Signature string
	Module    string
	Line      int
	// Sites: места вызова из других модулей.
	Sites []callSite
}

// orderKey: место метода в псевдослучайном порядке. Порядок задан зерном и
// самим методом, а не генератором случайных чисел: повторный запуск на той же
// выгрузке даёт ту же выборку, а добавление метода в конфигурацию не
// перетасовывает остальные. Ключ берётся из SHA-256: у быстрых хешей вроде
// FNV биты ключа слабо зависят от зерна, и разные зёрна давали бы почти одно
// и то же деление.
func orderKey(seed int, key string) uint64 {
	sum := sha256.Sum256([]byte(strconv.Itoa(seed) + "\x00" + key))
	return binary.BigEndian.Uint64(sum[:8])
}

// shuffled возвращает методы в псевдослучайном порядке orderKey.
func shuffled(methods []method, seed int) []method {
	out := append([]method(nil), methods...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := orderKey(seed, out[i].UID), orderKey(seed, out[j].UID)
		if a != b {
			return a < b
		}
		return out[i].Call < out[j].Call
	})
	return out
}

// picked: метод, отобранный в набор, со слоем выборки.
type picked struct {
	method
	Stratum string
}

// pickLibrary отбирает методы библиотеки: core самых вызываемых и rest
// случайных из остальных. Методы без вызовов из других модулей не берутся:
// описывать задачу не по чему. Из одного семейства (метод и его близнецы по
// месту исполнения) берётся один метод: иначе один и тот же случай входил бы
// в набор дважды. taken пополняется взятыми семействами.
func pickLibrary(methods []method, core, rest, seed int, taken map[string]bool) []picked {
	var called []method
	for _, m := range methods {
		if len(m.Sites) > 0 {
			called = append(called, m)
		}
	}
	byCalls := append([]method(nil), called...)
	sort.SliceStable(byCalls, func(i, j int) bool {
		if len(byCalls[i].Sites) != len(byCalls[j].Sites) {
			return len(byCalls[i].Sites) > len(byCalls[j].Sites)
		}
		return byCalls[i].Call < byCalls[j].Call
	})
	var out []picked
	take := func(pool []method, quota int, stratum string) {
		for _, m := range pool {
			if quota <= 0 {
				return
			}
			if f := callFamily(m.Call); !taken[f] {
				taken[f] = true
				out = append(out, picked{method: m, Stratum: stratum})
				quota--
			}
		}
	}
	take(byCalls, core, stratumCore)
	take(shuffled(called, seed), rest, stratumRest)
	return out
}

// siteOrder возвращает места вызова метода в порядке перебора: с
// псевдослучайного места по кругу. Порядок одинаков от запуска к запуску и
// не зависит от порядка, в котором места пришли из индекса; первым берётся
// место, из которого получился годный фрагмент.
func siteOrder(m method, seed int) []callSite {
	sites := append([]callSite(nil), m.Sites...)
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].Module != sites[j].Module {
			return sites[i].Module < sites[j].Module
		}
		return sites[i].Line < sites[j].Line
	})
	if len(sites) == 0 {
		return nil
	}
	start := int(orderKey(seed, m.UID) % uint64(len(sites)))
	return append(sites[start:], sites[:start]...)
}

// Суффиксы имён общих модулей, которыми стандарт разметки различает один и
// тот же модуль по месту исполнения. Метод с тем же именем в модуле-близнеце
// (ОбщегоНазначения и ОбщегоНазначенияКлиент) делает то же самое, и поиск,
// вернувший близнеца, не промахнулся.
var moduleSideSuffixes = []string{
	"клиентсервер", "вызовсервера", "клиент", "сервер", "глобальный", "повтисп", "полныеправа",
	"clientserver", "servercall", "client", "server", "global", "cached", "fullaccess",
}

// callFamily сводит выражение вызова к семейству: имя модуля без суффиксов
// места исполнения плюс имя метода, в нижнем регистре. Вызов через менеджер
// объекта (Справочники.Товары.Метод) и вызов метода глобального модуля
// семейством считаются как есть.
func callFamily(call string) string {
	lower := strings.ToLower(call)
	dot := strings.LastIndex(lower, ".")
	if dot < 0 || strings.Contains(lower[:dot], ".") {
		return lower
	}
	module, name := lower[:dot], lower[dot:]
	for trimmed := true; trimmed; {
		trimmed = false
		for _, suf := range moduleSideSuffixes {
			if rest := strings.TrimSuffix(module, suf); rest != module && rest != "" {
				module, trimmed = rest, true
				break
			}
		}
	}
	return module + name
}

// equivalents: какие методы каталога засчитываются друг за друга.
type equivalents struct {
	families map[string][]string
	names    map[string][]string
	// sections: секция каждого действующего метода.
	sections map[string]string
}

func newEquivalents(calls []string, sections map[string]string) equivalents {
	return equivalents{families: familyIndex(calls), names: nameIndex(calls), sections: sections}
}

// accepted: какие ответы поиска засчитываются для метода. Сам метод стоит
// первым, за ним:
//
//   - близнецы по семейству (тот же метод в модуле с другим местом исполнения);
//   - одноимённые методы другой секции: прикладная обёртка над методом
//     библиотеки (ПодборТоваровКлиентСервер.УстановитьПараметрДинамическогоСписка
//     рядом с ОбщегоНазначенияКлиентСервер.УстановитьПараметрДинамическогоСписка).
//     Запрос, написанный по вызову одного из них, второй не исключает.
//
// Одноимённый метод той же секции равноценным не считается: ДобавитьСтроку у
// дат запрета изменения и у шаблонов фискальных документов делают разное.
func (eq equivalents) accepted(call string) []string {
	out := []string{call}
	seen := map[string]bool{call: true}
	for _, twin := range eq.families[callFamily(call)] {
		if !seen[twin] {
			seen[twin] = true
			out = append(out, twin)
		}
	}
	for _, namesake := range eq.names[strings.ToLower(methodName(call))] {
		if !seen[namesake] && eq.sections[namesake] != eq.sections[call] {
			seen[namesake] = true
			out = append(out, namesake)
		}
	}
	return out
}

// nameIndex раскладывает вызовы каталога по имени метода (в нижнем регистре).
func nameIndex(calls []string) map[string][]string {
	out := map[string][]string{}
	for _, c := range calls {
		name := strings.ToLower(methodName(c))
		out[name] = append(out[name], c)
	}
	for name := range out {
		sort.Strings(out[name])
	}
	return out
}

// familyIndex раскладывает вызовы каталога по семействам.
func familyIndex(calls []string) map[string][]string {
	out := map[string][]string{}
	for _, c := range calls {
		f := callFamily(c)
		out[f] = append(out[f], c)
	}
	for f := range out {
		sort.Strings(out[f])
	}
	return out
}

// ambiguousNameFamilies: с какого числа семейств с одним именем метода имя
// считается неоднозначным. Метод ЗаполнитьИменаРеквизитовПоХозяйственнойОперации
// есть у десятков документов, и запрос, написанный по одному вызову, их не
// различает: под него подходит любой, а засчитывался бы один.
const ambiguousNameFamilies = 3

// ambiguousNames возвращает имена методов (в нижнем регистре), которые носят
// методы не меньше чем ambiguousNameFamilies разных семейств.
func ambiguousNames(calls []string) map[string]bool {
	families := map[string]map[string]bool{}
	for _, c := range calls {
		name := strings.ToLower(methodName(c))
		if families[name] == nil {
			families[name] = map[string]bool{}
		}
		families[name][callFamily(c)] = true
	}
	out := map[string]bool{}
	for name, fs := range families {
		if len(fs) >= ambiguousNameFamilies {
			out[name] = true
		}
	}
	return out
}
