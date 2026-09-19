package resolve

import "github.com/blessed2k/1C-WORKFLOW/internal/domain"

// DependencyEdgeKind — вид generic-связи без собственных атрибутов
// (архитектура §14: dependency_edge(kind, from_node, to_node, ...)).
type DependencyEdgeKind string

const (
	// DepFieldTypedBy — поле объекта метаданных типизировано ссылкой на
	// другой объект метаданных. Единственный вид, который DeriveDependencyEdges
	// производит: см. doc-комментарий функции про остальные четыре.
	DepFieldTypedBy DependencyEdgeKind = "field-typed-by"

	// Остальные известные виды рёбер, НЕ производимые этой
	// функцией — фактов не хватает на уровне одного компонента/Env этого
	// пакета, см. doc-комментарий DeriveDependencyEdges. Константы объявлены
	// здесь, чтобы пайплайн index писал их значения ровно так же, когда соберёт
	// эти рёбра сам, а не изобретал строки заново.
	DepSubsystemContains    DependencyEdgeKind = "subsystem-contains"
	DepExchangePlanContains DependencyEdgeKind = "exchange-plan-contains"
	DepTestReferencesSymbol DependencyEdgeKind = "test-references-symbol"
	DepEPFUsesObject        DependencyEdgeKind = "epf-uses-object"
)

// DependencyEdgeResult — одно generic-ребро (store.DependencyEdge без
// origin_file_id/layer — их знает вызывающий, у него источник файла и текущий слой).
type DependencyEdgeResult struct {
	Kind       DependencyEdgeKind
	FromKey    string // identity_key узла-источника — тот же IdentityKey, что вызывающий задал в MemberRef
	ToKey      string // identity_key узла-цели — ObjectRef.IdentityKey
	Confidence domain.Confidence
}

// DeriveDependencyEdges строит generic dependency_edge факты из того, что
// реально доступно фактам этого пакета: поле объекта метаданных
// типизировано ссылкой на другой объект (MemberRef.Types, как в исходнике
// XML, например "cfg:CatalogRef.Товары") резолвится через refTypeMType и
// Env.Object в edge field-typed-by. Мягкая цель: если объект типа не нашёлся
// в Env (ссылка на объект другого компонента, ещё не собранного в этот Env,
// или на несуществующий/непокрытый вид), ребро для этого типа просто не
// строится — не отправляем ToKey="".
//
// Остальные четыре вида рёбер этой функцией
// намеренно НЕ производятся — не хватает входных фактов на уровне resolve,
// а не забыты:
//
//   - subsystem-contains / exchange-plan-contains: содержимое (<Content>)
//     подсистемы и плана обмена — повторяющиеся, НЕ скалярные элементы XML.
//     parse/meta.MetadataObjectFact.Props хранит только "плоские скалярные
//     свойства" (facts.go), Content туда не попадает вообще. Нужно
//     расширение parse/meta (новый факт вида ContentFact с списком имён) —
//     это добавляет тот, кто трогает parse/meta следующим (задача, не
//     закрытая), resolve здесь потребитель факта, а не источник.
//   - test-references-symbol: требует знать, что ссылающийся модуль
//     принадлежит компоненту вида test-sources, а целевой символ — компоненту
//     основной конфигурации. Env этого пакета собирается НА ОДИН компонент
//     (§18.4: component_id — часть состава ключа разрешения); классификация
//     видов компонентов (workspace.Manifest.Component.Kind) и решение,
//     какие компоненты сливать для такого ребра, дело пайплайна (internal/index),
//     у которого есть манифест проекта, а не резолвера одного компонента.
//   - epf-uses-object: та же причина — EPF ссылается на объекты ДРУГОГО
//     компонента (основной конфигурации, через Component.UsesConfiguration
//     манифеста). Механизм внутри Env идентичен field-typed-by/manager-call
//     (тот же Env.Object), но сам Env для EPF-компонента должен быть
//     собран ПООБЪЕДИНЁННЫМ с объектами основной конфигурации — это решение
//     о том, что сливать, принимает пайплайн index при сборке EnvInput, не resolve.
func DeriveDependencyEdges(env Env) []DependencyEdgeResult {
	var out []DependencyEdgeResult
	for _, m := range env.Members() {
		if m.IdentityKey == "" {
			continue // без identity источника ребро вставить нечем
		}
		for _, t := range m.Types {
			mtype, nameNorm, ok := refTypeMType(t)
			if !ok {
				continue // примитив (xs:string и подобные) или неопознанный вид — не связь на объект
			}
			obj, found := env.Object(mtype, nameNorm)
			if !found {
				continue
			}
			out = append(out, DependencyEdgeResult{
				Kind: DepFieldTypedBy, FromKey: m.IdentityKey, ToKey: obj.IdentityKey, Confidence: domain.ConfidenceExact,
			})
		}
	}
	return out
}
