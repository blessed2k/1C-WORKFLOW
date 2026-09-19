package index

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// testMidRunHook — точка синхронизации, которой пользуются только тесты
// этого пакета (белый ящик): production-код никогда её не устанавливает.
var testMidRunHook func()

// totalSymbolCount — сумма методов и переменных по всем BSL-файлам корпуса
// (§17 п.7 — снимок «до»/«после» инкремента). Дёшево: линейный проход по уже
// разобранным в памяти mod.Methods/mod.Variables, без похода в store.
//
// Второе возвращаемое значение — ПОЛОН ли счёт. Это третий потребитель
// корпуса, которому нужны разобранные факты (ADR-028): у гидратированной
// записи их нет, и её символы в сумму не попадают. Отказать, как это делает
// buildEnvInput, здесь нельзя — снимок «до» берётся ровно тогда, когда корпус
// законно гидратирован, и считать ещё нечего. Поэтому слепота названа явно:
// на неполном счёте предохранитель §17 п.7 не срабатывает, потому что 0 «до»
// не значит, что символы пропали. Молча вернуть 0 значило бы выключить
// предохранитель на первом инкременте после перезапуска, ничего об этом не
// сказав.
func totalSymbolCount(corpus *componentCorpus) (int, bool) {
	n := 0
	complete := true
	for _, rec := range corpus.files {
		if rec.hydrated {
			complete = false
			continue
		}
		if rec.bslModule != nil {
			n += len(rec.bslModule.Methods) + len(rec.bslModule.Variables)
		}
	}
	return n, complete
}

// componentRunStats — то, что пайплайн одного компонента насчитал за проход:
// вход в Result.
type componentRunStats struct {
	filesChanged int
	filesRemoved int
	symbolCount  int
	diagnostics  []domain.Diagnostic
	counts       publishCounts
}

// runComponent прогоняет discover -> fingerprint -> parse -> normalize ->
// resolve -> derive -> validate -> publish (§17) для ОДНОГО компонента внутри
// уже открытой write-транзакции. corpus — резидентное состояние компонента
// (создаётся Service один раз и живёт между вызовами); full=true заставляет
// republish-ить вообще все обнаруженные файлы (используется для чистой
// пересборки и для первого прохода компонента).
func runComponent(ctx context.Context, tx *store.WriteTx, project domain.ProjectID, comp workspace.Component, corpus *componentCorpus, builtins *syntax.Index, cfg Config, full bool, stages *stageAccum) (componentRunStats, error) {
	workers := cfg.Workers
	var stats componentRunStats
	now := cfg.Now

	// testMidRunHook — синхронизация для тестов на last-known-good во время
	// rebuild (§18.1, §18.6 сценарий 3): вызывается ровно один раз посреди
	// уже открытой, ещё не закоммиченной write/rebuild-транзакции. nil в
	// production-пути.
	if testMidRunHook != nil {
		testMidRunHook()
	}

	if err := tx.UpsertComponent(store.Component{
		ID: string(comp.ID), Kind: string(comp.Kind), Root: comp.Root,
		AppliesTo: string(comp.AppliesTo), ApplyOrder: comp.ApplyOrder, Display: comp.DetectedName,
	}); err != nil {
		return stats, fmt.Errorf("component %s: %w", comp.ID, err)
	}

	discoverStart := now()
	discovered, err := discoverComponent(comp.AbsRoot, comp.Include, comp.Exclude)
	if err != nil {
		return stats, fmt.Errorf("discover %s: %w", comp.ID, err)
	}
	if ctx.Err() != nil {
		return stats, ctx.Err()
	}
	stages.mark(now, "discover", discoverStart)

	fingerprintStart := now()
	byPath := make(map[string]discoveredFile, len(discovered))
	for _, d := range discovered {
		byPath[d.relPath] = d
	}

	// beforeSymbolCount — снимок ДО фингерпринта/парсинга (§17 п.7: «число
	// символов не упало на порядок при инкременте»). Сравнивается с
	// totalSymbolCount(corpus) после того, как corpus обновлён, но ДО
	// publishFiles — падение на порядок абортит транзакцию (return error ->
	// ROLLBACK), не публикует подозрительно урезанный результат.
	beforeSymbolCount, beforeCountComplete := totalSymbolCount(corpus)

	// fingerprint (§17 п.2): новые/изменённые определяются по size+mtime,
	// истина — content_hash, который считается только когда size/mtime
	// разошлись (или файл новый) — самый частый случай (ничего не менялось)
	// не читает диск вообще.
	var toRead []parseTask
	if full {
		for _, d := range discovered {
			toRead = append(toRead, parseTask{relPath: d.relPath, absPath: d.absPath})
		}
	} else {
		for _, d := range discovered {
			old, known := corpus.files[d.relPath]
			// Гидратированные записи (восстановленные из source_file, ADR-028)
			// попадают в toRead ВСЕ и всегда, а не только изменившиеся: как
			// только пайплайн по компоненту реально запустился, к моменту
			// buildEnvInput в корпусе не должно остаться ни одной записи без
			// разобранных фактов. Ради случая «изменений нет» шаг и делается —
			// тогда пайплайн не запускается вовсе и читать нечего.
			if !known || old.hydrated || old.parserVersion != ParserVersion ||
				old.size != d.size || old.mtimeNS != d.mtimeNS {
				toRead = append(toRead, parseTask{relPath: d.relPath, absPath: d.absPath})
			}
		}
	}

	var removed []string
	if !full {
		for rel := range corpus.files {
			if _, ok := byPath[rel]; !ok {
				removed = append(removed, rel)
			}
		}
		sort.Strings(removed)
	}

	// oldSnapshots — состояние файлов ДО этого прохода, только для тех, что
	// изменились или пропали: единственное, из чего можно посчитать дельту
	// имён (affected.go) ПОСЛЕ того, как corpus.files уже обновлён под новую
	// версию. Полноценной копии корпуса не нужно — только тронутые файлы.
	oldSnapshots := make(map[string]*fileRecord, len(toRead)+len(removed))
	for _, rel := range removed {
		oldSnapshots[rel] = corpus.files[rel]
	}
	stages.mark(now, "fingerprint", fingerprintStart)

	parseStart := now()
	// blobs: образы файлов, republish-нутых только из-за смены резолюции
	// (см. ниже). Образы разобранных файлов в карту не попадают: пул отдаёт
	// их писателю сразу, и blob уже лежит в транзакции.
	blobs := make(map[string]store.PreparedBlob)
	changedSet := make(map[string]bool, len(toRead))
	if len(toRead) > 0 {
		// parse (§17 п.3): bounded worker pool — чтение и разбор тысяч
		// файлов последовательно доминирует время cold-индексации (замерено
		// на ut_demo), параллелизм по числу ядер держит это в бюджете §28.
		// Образы уходят в blob прямо из пула, пока воркеры разбирают
		// остальное: blob адресуется хэшем и от source_file не зависит, а
		// образ неизменившегося файла дедуплицируется (ON CONFLICT DO
		// NOTHING). Откат транзакции уносит их вместе со всем прочим.
		results, poolErr := runParsePool(ctx, workers, toRead, func(b store.PreparedBlob) error {
			_, err := tx.PutPreparedBlob(b)
			return err
		})
		if poolErr != nil {
			return stats, fmt.Errorf("parse %s: %w", comp.ID, poolErr)
		}
		for _, r := range results {
			rec := r.rec
			d := byPath[rec.relPath]
			rec.size, rec.mtimeNS = d.size, d.mtimeNS
			old, known := corpus.files[rec.relPath]
			if known && !old.hydrated && old.parserVersion == rec.parserVersion &&
				old.contentHash == rec.contentHash {
				// mtime/size шевельнулись, содержимое — нет (touch без
				// правки): факты не republish-им, но резидентную запись
				// обновляем, чтобы следующий fingerprint не читал файл
				// заново по тому же поводу (истина только hash, §17 п.2).
				old.size, old.mtimeNS = d.size, d.mtimeNS
				continue
			}
			if known {
				oldSnapshots[rec.relPath] = old
			}
			corpus.files[rec.relPath] = rec
			changedSet[rec.relPath] = true
		}
	}
	for _, rel := range removed {
		delete(corpus.files, rel)
	}
	stages.mark(now, "parse", parseStart)

	resolveStart := now()
	// Инвариант §17 п.7: после fingerprint/parse (corpus.files уже отражает
	// новое состояние: изменения применены, удаления вычищены) число
	// символов не должно падать на порядок — иначе это, вероятнее всего,
	// массовая порча выгрузки (например, недокачанный DumpConfigToFiles) или
	// дефект пайплайна, и публиковать такой результат нельзя. Полная
	// пересборка не проверяется: там corpus строится с нуля и промежуточного
	// «до» нет по определению; маленькие компоненты (< 20 символов) не
	// проверяются — на них порядок величины ничего не говорит.
	// beforeCountComplete=false — корпус на входе был гидратирован (первый
	// прогон после перезапуска): снимок «до» неполон, сравнивать не с чем, и
	// предохранитель пропускается сознательно, а не по недосмотру (ADR-028).
	afterSymbolCount, _ := totalSymbolCount(corpus)
	if !full && beforeCountComplete && beforeSymbolCount >= 20 && afterSymbolCount*10 < beforeSymbolCount {
		return stats, fmt.Errorf(
			"компонент %s: число символов упало на порядок за инкремент (%d -> %d) — публикация отменена, похоже на массовую порчу выгрузки",
			comp.ID, beforeSymbolCount, afterSymbolCount)
	}

	layer := comp.Layer()
	envInput, err := buildEnvInput(project, comp.ID, layer, corpus)
	if err != nil {
		// Инвариант ADR-028 нарушен: в корпусе осталась гидратированная
		// запись. Возврат ошибки абортит write-транзакцию — публиковать
		// результаты разрешения имён, собранные без части модулей и
		// объектов, нельзя ни при каких обстоятельствах.
		return stats, err
	}
	env, err := resolve.NewEnv(envInput, builtins)
	if err != nil {
		return stats, fmt.Errorf("env %s: %w", comp.ID, err)
	}

	var republishSet map[string]bool
	if full {
		resolveAndUpdate(env, corpus, nil) // весь корпус — уже republish-ится целиком
		republishSet = changedSet
	} else {
		republishSet = incrementalRepublishSet(comp.ID, layer, env, corpus, changedSet, removed, oldSnapshots, cfg)
	}
	for _, rel := range removed {
		corpus.dropResolved(rel)
	}

	republish := make([]string, 0, len(republishSet))
	for rel := range republishSet {
		republish = append(republish, rel)
	}
	sort.Strings(republish)
	stages.mark(now, "resolve", resolveStart)

	publishStart := now()
	// Образ для файлов, republish-нутых ТОЛЬКО из-за смены резолюции (не сами
	// изменились), берём заново с диска — их байты не менялись, но
	// publishFiles обязан положить blob, а blob дедуплицируется по хэшу,
	// то есть повторное чтение не тратит место в БД, только время на диске.
	for _, rel := range republish {
		if changedSet[rel] {
			continue // образ записан пулом разбора
		}
		abs, err := workspace.SafeJoin(comp.AbsRoot, rel)
		if err != nil {
			return stats, fmt.Errorf("republish %s: %w", rel, err)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return stats, fmt.Errorf("чтение %s: %w", rel, err)
		}
		blob, err := store.PrepareBlob(data)
		if err != nil {
			return stats, fmt.Errorf("образ %s: %w", rel, err)
		}
		blobs[rel] = blob
	}

	out, err := publishFiles(tx, publishInput{
		project: project, component: comp.ID, layer: layer,
		corpus: corpus, blobs: blobs, resolve: corpus.resolved, env: env,
		tunables:  cfg.GraphTunables,
		workers:   workers,
		republish: republish, removed: removed,
	})
	if err != nil {
		return stats, err
	}
	stages.mark(now, "publish", publishStart)

	stats.filesChanged = len(republish)
	stats.filesRemoved = len(removed)
	stats.symbolCount = out.symbolCount
	stats.diagnostics = out.diagnostics
	stats.counts = out.counts
	return stats, nil
}

// incrementalRepublishSet считает affected set инкремента (§18.4): для
// каждого изменённого/удалённого файла — дельту имён -> AffectedKeys ->
// файлы, консультировавшие эти ключи (corpus.keyIndex, ДО его обновления
// resolveAndUpdate). Порог fallback (config.go): если предполагаемый объём
// пересчёта велик, весь компонент разрешается заново, как при full.
func incrementalRepublishSet(comp domain.ComponentID, layer domain.Layer, env resolve.Env, corpus *componentCorpus, changedSet map[string]bool, removed []string, oldSnapshots map[string]*fileRecord, cfg Config) map[string]bool {
	var keys []resolve.KeyHash
	for rel := range changedSet {
		keys = append(keys, deltaKeysForFile(comp, layer, env, corpus, rel, oldSnapshots[rel])...)
	}
	for _, rel := range removed {
		keys = append(keys, deltaKeysForFile(comp, layer, env, corpus, rel, oldSnapshots[rel])...)
	}

	affected := filesConsulting(corpus, keys)
	total := make(map[string]bool, len(changedSet)+len(affected))
	for rel := range changedSet {
		total[rel] = true
	}
	for rel := range affected {
		if _, isRemoved := oldSnapshotIsRemoved(rel, removed); !isRemoved {
			total[rel] = true
		}
	}

	affectedRefs := 0
	for rel := range total {
		affectedRefs += len(corpus.resolved[rel])
	}
	threshold := int(float64(corpus.totalRefs) * cfg.FallbackPct)
	if threshold < cfg.FallbackMinKeys {
		threshold = cfg.FallbackMinKeys
	}
	if affectedRefs > threshold || len(total) > len(corpus.files)/2 {
		// Fallback (18.4): большой affected set -> полный re-resolve
		// компонента дешевле точечного (порог — tunable, здесь его
		// приближение через долю файлов, раз store не отдаёт точное число
		// ссылок компонента без полного прохода).
		resolveAndUpdate(env, corpus, nil)
		full := make(map[string]bool, len(corpus.files))
		for rel := range changedSet {
			full[rel] = true
		}
		for rel := range corpus.resolved {
			// Удалённые файлы ещё лежат в corpus.resolved (dropResolved идёт
			// после этой функции), но в corpus.files их уже нет: republish
			// такого файла перечитывал бы его с диска и ронял прогон.
			if _, alive := corpus.files[rel]; !alive {
				continue
			}
			full[rel] = true // консервативно: любой файл мог сменить резолюцию
		}
		return full
	}

	totalList := make([]string, 0, len(total))
	for rel := range total {
		totalList = append(totalList, rel)
	}
	sort.Strings(totalList)
	changedRes := resolveAndUpdate(env, corpus, totalList)

	out := make(map[string]bool, len(changedSet)+len(changedRes))
	for rel := range changedSet {
		out[rel] = true
	}
	for _, rel := range changedRes {
		out[rel] = true
	}
	for _, rel := range filesOfAppearedObjects(corpus, changedSet, oldSnapshots) {
		out[rel] = true
	}
	return out
}

// filesOfAppearedObjects: файлы каталога объекта, чей XML появился в этом
// инкременте (раньше файла не было). Модули и Form.xml такого объекта лежали
// без владельца, а их владелец (module.owner_object_id, form.owner_object_id),
// рёбра и бейджи объектного графа пишет только их собственная публикация:
// без переопубликования они расходились с чистой пересборкой (issue #14).
// Каталог объекта лежит рядом с его XML: "X.xml" и "X/..." (ADR-033).
// Исчезновение объекта сюда не входит: указатели на него обнуляет
// DeleteSourceFiles, рёбра и бейджи уходят каскадом.
func filesOfAppearedObjects(corpus *componentCorpus, changedSet map[string]bool, oldSnapshots map[string]*fileRecord) []string {
	dirs := map[string]bool{}
	for rel := range changedSet {
		rec := corpus.files[rel]
		if oldSnapshots[rel] != nil || rec == nil || rec.metaFacts.Object == nil || !strings.HasSuffix(rel, ".xml") {
			continue
		}
		dirs[strings.TrimSuffix(rel, ".xml")+"/"] = true
	}
	if len(dirs) == 0 {
		return nil
	}
	// Каталоги ищутся по префиксам пути файла до каждого "/": проход линеен
	// по корпусу (глубина пути мала), а не корпус на число новых объектов.
	var out []string
	for rel := range corpus.files {
		for i := strings.IndexByte(rel, '/'); i >= 0; {
			if dirs[rel[:i+1]] {
				out = append(out, rel)
				break
			}
			next := strings.IndexByte(rel[i+1:], '/')
			if next < 0 {
				break
			}
			i += next + 1
		}
	}
	sort.Strings(out)
	return out
}

func oldSnapshotIsRemoved(rel string, removed []string) (string, bool) {
	for _, r := range removed {
		if r == rel {
			return r, true
		}
	}
	return "", false
}

// deltaKeysForFile строит AffectedKeys для одного изменившегося/удалённого
// файла: BSL-модуль — обычная дельта имён; XML общего модуля — дельта по
// ВСЕМ его экспортным именам через связанный Module.bsl.
func deltaKeysForFile(comp domain.ComponentID, layer domain.Layer, env resolve.Env, corpus *componentCorpus, rel string, old *fileRecord) []resolve.KeyHash {
	newRec := corpus.files[rel] // nil для удалённых

	isBSL := (old != nil && old.bslModule != nil) || (newRec != nil && newRec.bslModule != nil)
	if isBSL {
		var info bsl.ModuleInfo
		var ownerType string
		if old != nil {
			info, ownerType = old.moduleInfo, old.moduleInfo.OwnerType
		}
		if newRec != nil {
			info, ownerType = newRec.moduleInfo, newRec.moduleInfo.OwnerType
		}
		var oldM, newM *bsl.Module
		if old != nil {
			oldM = old.bslModule
		}
		if newRec != nil {
			newM = newRec.bslModule
		}
		return affectedByChange(comp, layer, env, rel, info, oldM, newM, ownerType)
	}

	// XML общего модуля: смотрим Object.MType, старое и новое значение
	// Registry.Global по обеим версиям (файл мог только что стать/перестать
	// общим модулем — маловероятно, но дёшево учесть обе стороны).
	var oldObj, newObj *fileRecord
	if old != nil && old.metaFacts.Object != nil && old.metaFacts.Object.MType == "CommonModule" {
		oldObj = old
	}
	if newRec != nil && newRec.metaFacts.Object != nil && newRec.metaFacts.Object.MType == "CommonModule" {
		newObj = newRec
	}
	if oldObj == nil && newObj == nil {
		return nil
	}
	obj := newObj
	if obj == nil {
		obj = oldObj
	}
	bslPath := commonModuleBSLPath(obj.metaFacts.Object.NameDisplay)
	bslRec := corpus.files[bslPath]
	if bslRec == nil || bslRec.bslModule == nil {
		return nil
	}
	oldGlobal, newGlobal := false, false
	if oldObj != nil && oldObj.metaFacts.ModuleRegistry != nil {
		oldGlobal = oldObj.metaFacts.ModuleRegistry.Global
	}
	if newObj != nil && newObj.metaFacts.ModuleRegistry != nil {
		newGlobal = newObj.metaFacts.ModuleRegistry.Global
	}
	return affectedByCommonModuleXMLChange(comp, layer, bslPath, bslRec.bslModule, oldGlobal, newGlobal, obj.metaFacts.Object.NameNorm)
}
