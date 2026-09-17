package index

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// deepFileFacts — полный набор фактов ОДНОГО файла, как их держит пайплайн в
// памяти ПЕРЕД публикацией (fileRecord + resolvedRef из corpus.resolved):
// не только присутствие identity-ключей, но и содержимое каждой записи
// (Span, Export, Directive, Resolution, Confidence, ...). Это и есть
// «логический дамп» на уровне, доступном ИЗНУТРИ зоны index — store не
// выставляет bulk SELECT (D02), поэтому сравнение идёт ДО публикации, не
// после (ревью, требование к property-тесту).
//
// Честная граница покрытия (найдена ревью мутацией: пропуск первого элемента
// в publishRegisterAccess прошёл этот тест зелёным): снимок строится из
// corpus/resolved — того, что пайплайн ПОСЧИТАЛ нужным опубликовать, а не из
// того, что реально осело в store. Регрессия внутри publish*.go (пропущенный
// tx.InsertXxx, урезанный цикл) этому тесту невидима — её ловит отдельный
// счётчик TestPublishedCountsMatchDerivedFacts (publishcount_test.go),
// сравнивающий ComponentResult.Counts с независимо пересчитанным числом
// фактов. Два теста закрывают разные половины одного риска, не дублируют
// друг друга.
type deepFileFacts struct {
	ContentHash string
	IsBSL       bool
	Symbols     []domain.Symbol
	Refs        []resolvedRef

	HasMeta     bool
	ObjectMType string
	ObjectUUID  string
	ObjectName  string
	ObjectProps map[string]string
	MemberNames []string // NameNorm отсортированы — состав членов, без дублирования decode-логики meta
}

// snapshotCorpus строит канонический, отсортированный слепок ВСЕГО корпуса
// компонента: детерминированный вход для reflect.DeepEqual. Каждый список
// внутри файла тоже отсортирован — сравнение не зависит от порядка обхода
// map или порядка republish.
func snapshotCorpus(project domain.ProjectID, component domain.ComponentID, layer domain.Layer, corpus *componentCorpus) map[string]deepFileFacts {
	out := make(map[string]deepFileFacts, len(corpus.files))
	for rel, rec := range corpus.files {
		f := deepFileFacts{ContentHash: rec.contentHash}
		if rec.bslModule != nil {
			f.IsBSL = true
			f.Symbols = append([]domain.Symbol(nil), buildSymbols(project, component, layer, rel, rec.bslModule)...)
			sort.Slice(f.Symbols, func(i, j int) bool { return f.Symbols[i].UID < f.Symbols[j].UID })
			f.Refs = append([]resolvedRef(nil), corpus.resolved[rel]...)
			sort.Slice(f.Refs, func(i, j int) bool { return f.Refs[i].raw.Span.StartByte < f.Refs[j].raw.Span.StartByte })
		}
		if rec.metaFacts.Object != nil {
			f.HasMeta = true
			f.ObjectMType = rec.metaFacts.Object.MType
			f.ObjectUUID = rec.metaFacts.Object.UUID
			f.ObjectName = rec.metaFacts.Object.NameNorm
			f.ObjectProps = rec.metaFacts.Object.Props
			for _, m := range rec.metaFacts.Members {
				f.MemberNames = append(f.MemberNames, m.NameNorm)
			}
			sort.Strings(f.MemberNames)
		}
		out[rel] = f
	}
	return out
}

// diffSnapshots сравнивает два слепка ПОЛНОСТЬЮ — и что в A есть лишнего
// сверх B, и наоборот, и содержимое каждого общего файла. Возвращает пустой
// срез при полном совпадении.
func diffSnapshots(a, b map[string]deepFileFacts) []string {
	var diffs []string
	keys := make(map[string]bool, len(a)+len(b))
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		fa, okA := a[k]
		fb, okB := b[k]
		switch {
		case okA && !okB:
			diffs = append(diffs, fmt.Sprintf("%s: есть в A, нет в B", k))
		case !okA && okB:
			diffs = append(diffs, fmt.Sprintf("%s: есть в B, нет в A", k))
		case !reflect.DeepEqual(fa, fb):
			diffs = append(diffs, fmt.Sprintf("%s: содержимое различается\n  A=%+v\n  B=%+v", k, fa, fb))
		}
	}
	return diffs
}
