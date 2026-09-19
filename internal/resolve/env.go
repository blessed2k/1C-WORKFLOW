package resolve

import (
	"fmt"
	"sync"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
)

// ModuleEntry — один модуль компонента: идентичность (§14/§15: module +
// аспекты module_context/module_code), символы, объявленные в нём, и, для
// общих модулей, свойства из XML (parse/meta.ModuleRegistryFact — тот же
// тип, без копии). Строится вызывающим (internal/index) из фактов parse/bsl и
// parse/meta; сам resolve модулей не парсит.
type ModuleEntry struct {
	// ModulePath — канонический путь модуля (domain.NormalizeModulePath),
	// используется как identity локального модуля в ключе разрешения.
	ModulePath string
	Kind       bsl.ModuleKind
	// NameNorm — нормализованное имя ОБЩЕГО модуля (для ModuleCommon);
	// пусто у прочих видов.
	NameNorm string
	// OwnerMType/OwnerNameNorm — владелец объектного/менеджерного/... модуля
	// в терминах parse/meta.MetadataObjectFact.MType (см. bslCollectionToMType).
	OwnerMType    string
	OwnerNameNorm string
	// Registry — свойства общего модуля из его XML; nil для остальных видов.
	Registry *meta.ModuleRegistryFact
	Layer    domain.Layer
	Symbols  []domain.Symbol
}

// ObjectRef — объект метаданных, видимый резолверу: MType+имя для
// менеджерных обращений (Справочники.X, Движения.Y) и IdentityKey — строка,
// которой store адресует свой node (резолвер её не изобретает, только
// передаёт насквозь как TargetKey результата).
type ObjectRef struct {
	MType       string
	NameNorm    string
	IdentityKey string
}

// MemberRef — член объекта метаданных (реквизит/ресурс/измерение/ТЧ),
// видимый резолверу: Types — типы КАК В ИСХОДНИКЕ XML (сырые XDTO-имена,
// например "cfg:CatalogRef.Товары", "xs:string" — meta.MetadataMemberFact.Types
// без изменений), используются для query-полей (DeriveQueryReference) и
// dependency_edge "поле типизировано объектом" (DeriveDependencyEdges).
// IdentityKey — как у ObjectRef: строку, которой store адресует свой node,
// резолвер не изобретает, только передаёт насквозь.
type MemberRef struct {
	ObjectMType    string
	ObjectNameNorm string
	Kind           string
	NameNorm       string
	Types          []string
	IdentityKey    string
}

// EnvInput — сырые факты одного компонента (и слоя), из которых строится Env.
type EnvInput struct {
	Component domain.ComponentID
	Layer     domain.Layer
	Modules   []ModuleEntry
	Objects   []ObjectRef
	Members   []MemberRef
}

// Env — отображение «ключ разрешения -> множество кандидатов» (§18.4):
// местный модуль, глобальные общие модули, экспорт конкретного общего
// модуля, менеджерный модуль объекта, platform builtins. Строится один раз
// на компонент/слой и переиспользуется для всех ссылок этого модуля.
type Env struct {
	component domain.ComponentID
	layer     domain.Layer

	modules map[string]*ModuleEntry // ModulePath -> модуль (identity локального модуля)
	byName  map[string]*ModuleEntry // NameNorm -> общий модуль (для qualified module call)
	global  []*ModuleEntry          // общие модули с Global=true, в порядке ввода (для deterministic rank)
	objects map[string]ObjectRef    // MType+"\x00"+NameNorm -> объект
	manager map[string]*ModuleEntry // OwnerMType+"\x00"+OwnerNameNorm -> модуль менеджера

	members     map[string]MemberRef // objectKey(ObjectMType,ObjectNameNorm)+"\x00"+NameNorm -> член
	membersList []MemberRef          // тот же набор, порядок ввода — для детерминированного обхода DeriveDependencyEdges

	builtins *syntax.Index
	// builtinIdx — кэш platform-lookup за указателем: Env копируется по
	// значению (возвращается из NewEnv как значение, а не *Env), а
	// sync.Mutex внутри значения, гуляющего по копиям, — гонка и провал
	// go vet (copylocks). Указатель на общий кэш безопасен к копированию
	// самого Env.
	builtinIdx *builtinIndex
}

// builtinEntry — только те поля internal/onec.SyntaxEntry, которые нужны
// резолверу (имя, доступность). internal/onec — старый слой (RuleNoLegacyInNew,
// internal/arch): разрешён только сам internal/syntax, поэтому его тип
// наружу не протаскивается — из internal/syntax.Index.GlobalMethod значения
// копируются в этот тип сразу при вызове, без импорта internal/onec.
type builtinEntry struct {
	NameRu       string
	NameEn       string
	Availability string
}

type builtinLookup struct {
	entry builtinEntry
	ok    bool
}

type builtinIndex struct {
	mu    sync.Mutex
	cache map[string]builtinLookup
}

// NewEnv строит Env из сырых фактов компонента. builtins может быть nil —
// тогда platform builtins не разрешаются (шаг 4 §19.1 пропускается, ссылки
// такого вида остаются unresolved); это удобно для фикстур, которым
// справочник платформы не нужен.
func NewEnv(input EnvInput, builtins *syntax.Index) (Env, error) {
	if input.Component == "" {
		return Env{}, fmt.Errorf("resolve.NewEnv: пустой component")
	}
	env := Env{
		component:  input.Component,
		layer:      input.Layer,
		modules:    make(map[string]*ModuleEntry, len(input.Modules)),
		byName:     make(map[string]*ModuleEntry),
		objects:    make(map[string]ObjectRef, len(input.Objects)),
		manager:    make(map[string]*ModuleEntry),
		members:    make(map[string]MemberRef, len(input.Members)),
		builtins:   builtins,
		builtinIdx: &builtinIndex{cache: make(map[string]builtinLookup)},
	}
	for i := range input.Modules {
		m := &input.Modules[i]
		if m.ModulePath == "" {
			return Env{}, fmt.Errorf("resolve.NewEnv: модуль без пути (индекс %d)", i)
		}
		env.modules[m.ModulePath] = m
		if m.Kind == bsl.ModuleCommon && m.NameNorm != "" {
			env.byName[m.NameNorm] = m
			if m.Registry != nil && m.Registry.Global {
				env.global = append(env.global, m)
			}
		}
		if m.OwnerMType != "" && m.OwnerNameNorm != "" {
			env.manager[objectKey(m.OwnerMType, m.OwnerNameNorm)] = m
		}
	}
	for _, o := range input.Objects {
		if o.MType == "" || o.NameNorm == "" {
			continue
		}
		env.objects[objectKey(o.MType, o.NameNorm)] = o
	}
	for _, m := range input.Members {
		if m.ObjectMType == "" || m.ObjectNameNorm == "" || m.NameNorm == "" {
			continue
		}
		env.members[objectKey(objectKey(m.ObjectMType, m.ObjectNameNorm), m.NameNorm)] = m
		env.membersList = append(env.membersList, m)
	}
	return env, nil
}

func objectKey(mtype, nameNorm string) string {
	return mtype + "\x00" + nameNorm
}

// Module возвращает модуль по его идентичности (канонический путь).
func (e Env) Module(modulePath string) (*ModuleEntry, bool) {
	m, ok := e.modules[modulePath]
	return m, ok
}

// CommonModuleByName возвращает общий модуль по нормализованному имени.
func (e Env) CommonModuleByName(nameNorm string) (*ModuleEntry, bool) {
	m, ok := e.byName[nameNorm]
	return m, ok
}

// ManagerModule возвращает модуль менеджера объекта (mtype+имя объекта).
func (e Env) ManagerModule(mtype, ownerNameNorm string) (*ModuleEntry, bool) {
	m, ok := e.manager[objectKey(mtype, ownerNameNorm)]
	return m, ok
}

// Object возвращает объект метаданных по mtype+имени.
func (e Env) Object(mtype, nameNorm string) (ObjectRef, bool) {
	o, ok := e.objects[objectKey(mtype, nameNorm)]
	return o, ok
}

// ObjectByAnyRegisterKind ищет объект среди видов регистров (registerMTypes)
// по имени — используется для Движения.Х, где сам вид регистра из BSL не
// виден (parser MetaType == ""). ok=false, если совпадений нет ИЛИ больше
// одного (неоднозначность вида регистра резолвер не разрешает, объект
// остаётся unresolved).
func (e Env) ObjectByAnyRegisterKind(nameNorm string) (ObjectRef, bool) {
	var found ObjectRef
	count := 0
	for _, mtype := range registerMTypes {
		if o, ok := e.Object(mtype, nameNorm); ok {
			found = o
			count++
		}
	}
	if count != 1 {
		return ObjectRef{}, false
	}
	return found, true
}

// Member возвращает член объекта метаданных по mtype+имени объекта+имени члена.
func (e Env) Member(objectMType, objectNameNorm, memberNameNorm string) (MemberRef, bool) {
	m, ok := e.members[objectKey(objectKey(objectMType, objectNameNorm), memberNameNorm)]
	return m, ok
}

// Members возвращает все члены компонента в порядке добавления —
// используется DeriveDependencyEdges для обхода "поле типизировано объектом".
func (e Env) Members() []MemberRef {
	return e.membersList
}

// GlobalModules возвращает общие модули с Global=true в порядке добавления
// (детерминированный обход для стабильного rank кандидатов).
func (e Env) GlobalModules() []*ModuleEntry {
	return e.global
}

// lookupBuiltin ищет глобальный метод платформы, кэшируя результат: сам
// internal/syntax.Index.GlobalMethod — линейный обход ~23 тыс. записей,
// а словарь имён в реальной конфигурации конечен и на порядки меньше числа
// ссылок на них (упрощение, задокументировано намеренно). Кэш общий на Env,
// поэтому под мьютексом: Env разделяется между воркерами инкремента.
func (e Env) lookupBuiltin(nameNorm string) (builtinEntry, bool) {
	if e.builtins == nil {
		return builtinEntry{}, false
	}
	idx := e.builtinIdx
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if v, ok := idx.cache[nameNorm]; ok {
		return v.entry, v.ok
	}
	raw, ok := e.builtins.GlobalMethod(nameNorm) // raw — onec.SyntaxEntry по выводу типов, без импорта пакета
	entry := builtinEntry{NameRu: raw.NameRu, NameEn: raw.NameEn, Availability: raw.Availability}
	idx.cache[nameNorm] = builtinLookup{entry: entry, ok: ok}
	return entry, ok
}
