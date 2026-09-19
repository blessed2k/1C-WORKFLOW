package index

import (
	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
)

// fileRecord — резидентное состояние одного файла компонента между запусками
// пайплайна: то, что позволяет инкременту не перечитывать и не
// перепарсивать файлы, которых изменение не коснулось (§18.4, §26 — LRU/
// «карта модулей» разрешена как кэш поколения). Хранится в памяти Service,
// не в SQLite: store фактов на выборку не отдаёт, поэтому EnvInput
// собирается из ЭТОГО кэша, а не запросом к store.
type fileRecord struct {
	relPath     string
	size        int64
	mtimeNS     int64
	contentHash string

	// parserVersion — версия парсера, которой получены факты этой записи
	// (ParserVersion у разобранной, значение из source_file.parser_version у
	// восстановленной). Запись, разобранная старым парсером, считается
	// изменённой: иначе после обновления сервера индекс объявил бы себя
	// свежим и отдавал факты, которых новый парсер уже не производит (§4).
	parserVersion int

	// hydrated — запись восстановлена из source_file при первом обращении к
	// компоненту (ADR-028) и несёт ТОЛЬКО фингерпринт: ни bslModule, ни
	// metaFacts у неё нет. Потребители делятся на две группы: precheck и
	// отбор toRead читают size/mtime/contentHash и такой записью
	// довольствуются; buildEnvInput разыменовывает факты и обязан на ней
	// упасть, а не собрать пустое окружение.
	hydrated bool

	// bslModule заполнен для .bsl-файлов; nil для остальных.
	bslModule *bsl.Module
	// moduleInfo — вид модуля и владелец, выведенные из пути (bsl.ClassifyModule).
	moduleInfo bsl.ModuleInfo

	// metaFacts заполнен для XML-файлов метаданных; нулевое значение для
	// остальных (Facts{} с Object==nil и т.д. — meta.Kind различает пустой
	// результат от неразобранного вовсе файла через metaKind).
	metaFacts meta.Facts
	metaKind  meta.Kind

	diagnostics []domain.Diagnostic
}

// componentCorpus — весь резидентный корпус одного компонента: файлы,
// собранные символы (для Env) и результаты разрешения ссылок (для сравнения
// «что изменилось» без обращения к store, §18.4).
type componentCorpus struct {
	id domain.ComponentID

	// files — по relPath, единственный источник правды о том, что уже
	// проиндексировано.
	files map[string]*fileRecord

	// resolved — RawRef+Result последнего разрешения по relPath модуля,
	// откуда взята ссылка.
	resolved map[string][]resolvedRef

	// keyIndex — обратный индекс resolution_dep (§18.4), только в памяти:
	// key_hash -> множество relPath файлов, чьи ссылки консультировали этот
	// ключ (включая пустые результаты). Позволяет находить affected set БЕЗ
	// полного re-resolve компонента на каждый инкремент — store такой
	// SELECT не отдаёт, а держать его в памяти рядом с resolved дёшево:
	// он строится из того же прохода, что уже вычисляет ConsultedKeys.
	keyIndex map[resolve.KeyHash]map[string]bool
	// hydrationDone — попытка восстановить files из source_file уже сделана
	// (успешно или нет, см. ensureHydrated). Отдельный флаг, а не проверка
	// len(files): у компонента может честно не быть ни одного файла, и
	// повторять неудачную попытку на каждом precheck незачем.
	hydrationDone bool

	// totalRefs — сумма len(resolved[...]) по всем файлам: числитель для
	// порога fallback (18.4) max(FallbackMinKeys, FallbackPct*totalRefs).
	totalRefs int
}

// resolvedRef — одна ссылка с её текущим результатом разрешения.
type resolvedRef struct {
	raw    resolve.RawRef
	result resolve.Result
}

func newComponentCorpus(id domain.ComponentID) *componentCorpus {
	return &componentCorpus{
		id:       id,
		files:    make(map[string]*fileRecord),
		resolved: make(map[string][]resolvedRef),
		keyIndex: make(map[resolve.KeyHash]map[string]bool),
	}
}

// setResolved заменяет resolved[relPath] и ведёт keyIndex/totalRefs в ногу:
// снимает записи старого набора ссылок этого файла и добавляет новые. Это
// ЕДИНСТВЕННАЯ точка, которая должна менять corpus.resolved — если писать в
// него напрямую, keyIndex разойдётся с реальностью.
func (c *componentCorpus) setResolved(relPath string, refs []resolvedRef) {
	if old, ok := c.resolved[relPath]; ok {
		c.totalRefs -= len(old)
		for _, r := range old {
			for _, k := range r.result.ConsultedKeys {
				if set := c.keyIndex[k]; set != nil {
					delete(set, relPath)
					if len(set) == 0 {
						delete(c.keyIndex, k)
					}
				}
			}
		}
	}
	if len(refs) == 0 {
		delete(c.resolved, relPath)
		return
	}
	c.resolved[relPath] = refs
	c.totalRefs += len(refs)
	for _, r := range refs {
		for _, k := range r.result.ConsultedKeys {
			set := c.keyIndex[k]
			if set == nil {
				set = make(map[string]bool)
				c.keyIndex[k] = set
			}
			set[relPath] = true
		}
	}
}

// dropResolved снимает файл из resolved/keyIndex совсем — вызывается при
// удалении файла (fingerprint нашёл его пропавшим).
func (c *componentCorpus) dropResolved(relPath string) { c.setResolved(relPath, nil) }
