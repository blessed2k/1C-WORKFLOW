package app

import (
	"context"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// Ходовые методы библиотеки: те, которыми прикладной код этой конфигурации
// пользуется чаще всего. Готовый метод агент теряет не на поиске, а до него:
// не подумал искать. То, что лежит перед глазами, он использует, не решая
// «пойти поискать», поэтому короткий список отдаётся вместе с ответом, за
// которым агент и так приходит (get_context_for_task, find_api без запроса).
//
// Библиотека огромна, а пользуются малой её частью: числа для УТ 11.5 в
// docs/tools-index.md (раздел find_api), замер: go run ./evals/findapi core.

const (
	// apiCoreLimit: сколько методов в списке. Список читает модель, и длинный
	// перечень она читает хуже; сотня имён это около трёх тысяч знаков.
	apiCoreLimit = 100
	// apiCoreMinObjects: метод, которым пользуются меньше чем в стольких
	// прикладных объектах, ходовым не считается.
	apiCoreMinObjects = 3
)

// APICoreModule: модуль библиотеки и его ходовые методы, самые ходовые
// первыми. Только имена: сигнатуру и описание отдаёт get_symbol или find_api
// с module.
type APICoreModule struct {
	Module  string   `json:"module"`
	Methods []string `json:"methods"`
}

// APICoreItem: ходовые методы библиотеки (items[0]).
type APICoreItem struct {
	BSPVersion string `json:"bspVersion,omitempty"`
	// Modules: модули в порядке их самого ходового метода.
	Modules []APICoreModule `json:"modules"`
	// Methods: сколько методов в списке; Candidates: сколько методов
	// библиотеки прикладной код зовёт вообще (из тех, что могли попасть в
	// список).
	Methods    int `json:"methods"`
	Candidates int `json:"candidates"`
	// Coverage: какую долю вызовов методов-кандидатов из прикладного кода
	// закрывает список (0..1). В счёт идут вызовы, которые индекс разрешил до
	// действующего метода программного интерфейса библиотеки; обработчики
	// событий и устаревшие методы кандидатами не являются.
	Coverage float64 `json:"coverage"`
}

// apiCore: посчитанный список. Срезы после расчёта не меняются: ответы делят
// их без копирования.
type apiCore struct {
	modules    []APICoreModule
	methods    int
	candidates int
	coverage   float64
	// apiMethods: сколько методов программного интерфейса было в индексе,
	// по которому список считался (см. core).
	apiMethods int
}

// core отдаёт ходовые методы библиотеки, считая их при первом обращении.
// Метод тем ходовее, чем в большем числе прикладных объектов его зовут.
// Считаются объекты, а не вызовы и не файлы: метод, который зовут тысячу раз
// из одного модуля обмена, общим помощником не является, а документ с модулем
// объекта и двумя формами это один объект, а не три. Вызовы из самой
// библиотеки не в счёт: её внутренняя кухня о том, что нужно прикладному
// коду, не говорит.
//
// В список не идут устаревшие методы и обработчики событий (ПриСозданииНаСервере
// подсистемы зовут из каждой формы, но это обвязка формы, а не помощник).
//
// упрощение: расчёт идёт под своим замком (coreMu) внутри читающей транзакции
// вызова, который пришёл первым; ждущие отмену своего контекста не слушают,
// как и ждущие постройку индекса слов. На УТ 11.5 это около 1,3 с (560 тысяч
// разрешённых вызовов). Чтобы не платить их после каждой правки файла, список
// прошлого поколения индекса переносится в новое, пока число методов
// программного интерфейса не изменилось: от правки одного модуля ходовые
// методы не меняются. Путь снятия обоих упрощений: считать при переиндексации
// и хранить в индексе.
func (ix *apiIndex) core(tx *store.ReadTx) (*apiCore, error) {
	ix.coreMu.Lock()
	defer ix.coreMu.Unlock()
	if ix.coreDone != nil {
		return ix.coreDone, nil
	}
	if ix.coreStale != nil && ix.coreStale.apiMethods == len(ix.methods) {
		ix.coreDone = ix.coreStale
		return ix.coreDone, nil
	}
	var ids []int64
	byID := map[int64]int32{}
	for i, m := range ix.methods {
		if !m.library || m.deprecated || apiDraftEventWords[apiFirstWord(m.row.NameDisplay)] {
			continue
		}
		ids = append(ids, m.row.SymbolID)
		byID[m.row.SymbolID] = int32(i)
	}
	var rows []store.CallerFileRow
	if len(ids) > 0 { // методов библиотеки нет: считать нечего, базу не читаем
		var err error
		if rows, err = tx.CallerFilesOf(ids); err != nil {
			return nil, err
		}
	}
	objects := map[int32]map[string]struct{}{}
	calls := map[int32]int{}
	total := 0
	for _, r := range rows {
		object, library := ix.callerObject(r.ComponentID, r.RelPath)
		if library {
			continue
		}
		m := byID[r.CalleeID]
		if objects[m] == nil {
			objects[m] = map[string]struct{}{}
		}
		objects[m][object] = struct{}{}
		calls[m] += r.Calls
		total += r.Calls
	}
	ranked := make([]int32, 0, len(objects))
	for m, set := range objects {
		if len(set) >= apiCoreMinObjects {
			ranked = append(ranked, m)
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if len(objects[a]) != len(objects[b]) {
			return len(objects[a]) > len(objects[b])
		}
		if calls[a] != calls[b] {
			return calls[a] > calls[b]
		}
		if ca, cb := ix.methods[a].call, ix.methods[b].call; ca != cb {
			return ca < cb
		}
		return a < b
	})
	if len(ranked) > apiCoreLimit {
		ranked = ranked[:apiCoreLimit]
	}
	core := &apiCore{modules: []APICoreModule{}, methods: len(ranked), candidates: len(objects), apiMethods: len(ix.methods)}
	at := map[string]int{}
	covered := 0
	for _, m := range ranked {
		im := ix.methods[m]
		covered += calls[m]
		i, seen := at[im.owner]
		if !seen {
			i = len(core.modules)
			at[im.owner] = i
			core.modules = append(core.modules, APICoreModule{Module: im.owner})
		}
		core.modules[i].Methods = append(core.modules[i].Methods, im.row.NameDisplay)
	}
	if total > 0 {
		core.coverage = float64(covered) / float64(total)
	}
	ix.coreDone = core
	return core, nil
}

// apiFirstWord: первое слово идентификатора в нижнем регистре.
func apiFirstWord(name string) string {
	if words := apiWords(name); len(words) > 0 {
		return words[0]
	}
	return ""
}

// callerObject относит вызывающий файл к объекту конфигурации и говорит,
// библиотечный ли он. Объект выводится из пути: каталог коллекции и имя
// объекта (CommonModules/Имя, Catalogs/Имя): модуль объекта, модули его форм
// и команд это один объект. Файл вне каталогов объектов (модуль приложения,
// сеанса) считается прикладным объектом сам по себе.
//
// Переопределяемый модуль библиотеки (ОбщегоНазначенияПереопределяемый) в
// состав библиотеки входит, но код в нём пишет разработчик конфигурации:
// вызовы оттуда прикладные.
func (ix *apiIndex) callerObject(componentID, relPath string) (object string, library bool) {
	dir, rest, ok := strings.Cut(relPath, "/")
	if !ok {
		return componentID + "\x00" + relPath, false
	}
	name, _, _ := strings.Cut(rest, "/")
	name = strings.TrimSuffix(name, ".xml")
	object = componentID + "\x00" + dir + "/" + name
	kind, known := domain.MetaKindByDumpDir(dir)
	if !known || name == "" {
		return object, false
	}
	if kind.MType == "CommonModule" {
		low := strings.ToLower(name)
		for _, marker := range apiOverridableMarkers {
			if strings.Contains(low, marker) {
				return object, false
			}
		}
	}
	return object, ix.libraryObjects[apiObjectKey(componentID, kind.MType+"."+name)]
}

// CoreMethods отдаёт ходовые методы библиотеки этой конфигурации. Библиотеки
// в выгрузке нет или прикладной код её не зовёт: список пуст, это не ошибка.
// Предупреждения те же, что у поиска: по ним видно, что состав библиотеки
// прочитан не полностью и список мог перекоситься.
func (s *APIService) CoreMethods(ctx context.Context) (Response[APICoreItem], error) {
	op, err := s.projects.Active(ctx)
	if err != nil {
		return Response[APICoreItem]{}, err
	}
	type txResult struct {
		item APICoreItem
		gen  domain.Generation
		warn []Warning
	}
	res, snap, err := ReadSnapshot(ctx, op, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen
		ix, ierr := s.indexes.get(op.Entry.ID, tx, gen)
		if ierr != nil {
			return out, ierr
		}
		out.warn = append([]Warning(nil), ix.warn...)
		core, cerr := ix.core(tx)
		if cerr != nil {
			return out, cerr
		}
		out.item = APICoreItem{
			BSPVersion: ix.version, Modules: core.modules,
			Methods: core.methods, Candidates: core.candidates, Coverage: core.coverage,
		}
		return out, nil
	})
	if err != nil {
		return Response[APICoreItem]{}, err
	}
	resp := Response[APICoreItem]{Generation: res.gen, Items: []APICoreItem{res.item}, TotalCount: 1, Warnings: res.warn}
	return withSnapshot(resp, snap), nil
}

// CoreMethodsIfReady отдаёт ходовые методы библиотеки активного проекта, если
// они уже посчитаны, и ничего не считает и не ждёт: индекс слов занят
// постройкой или список ещё не считался: ready=false. Нужен инструменту,
// который обязан отвечать мгновенно (server_info); считает список
// CoreMethods. Список может быть от прошлого поколения индекса: для подсказки
// это годится.
func (s *APIService) CoreMethodsIfReady() (item APICoreItem, ready bool) {
	project := s.projects.ActiveState().Project
	if project == "" || !s.indexes.mu.TryLock() {
		return APICoreItem{}, false
	}
	ix := s.indexes.indexes[project]
	s.indexes.mu.Unlock()
	if ix == nil || !ix.coreMu.TryLock() {
		return APICoreItem{}, false
	}
	core := ix.coreDone
	ix.coreMu.Unlock()
	if core == nil {
		return APICoreItem{}, false
	}
	return APICoreItem{
		BSPVersion: ix.version, Modules: core.modules,
		Methods: core.methods, Candidates: core.candidates, Coverage: core.coverage,
	}, true
}
