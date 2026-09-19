package index

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// discoveredFile — один файл, найденный обходом корня компонента: только то,
// что нужно шагу fingerprint (§17 п.1-2), без чтения содержимого.
type discoveredFile struct {
	relPath string // канонический путь относительно корня компонента, со слешами
	absPath string
	size    int64
	mtimeNS int64
}

// discoveredMeta — relPath/size/mtimeNS без absPath: то немногое, что нужно
// precheckChangedCount (§18.5) для сравнения с corpus.files. Отдельный тип
// вместо discoveredFile с пустым absPath — чтобы на месте вызова было видно,
// что absPath здесь не считался и использовать его нельзя.
type discoveredMeta struct {
	relPath string
	size    int64
	mtimeNS int64
}

// walkComponent — общий обход корня компонента: фильтрация служебных
// каталогов/файлов (workspace.IsIgnored — та же функция, что скрывает
// workspace.SkipDir/SkipFile: файловая половина списка неиндексируемого)
// и include/exclude манифеста через workspace.MatchPath
// (единственный матчер шаблонов во всём коде). Один источник истины о ТОМ,
// какие файлы входят в компонент — используется и discoverComponent (нужен
// безопасный absPath для чтения содержимого), и discoverComponentMeta
// (только сравнение метаданных, precheck): расхождение фильтров между ними
// было бы отдельным классом бага (precheck и реальный discover видят разные
// файлы), поэтому сама фильтрация здесь ровно одна на обоих путях.
func walkComponent(absRoot string, include, exclude []string, visit func(relSlash, rel string, d fs.DirEntry) error) error {
	return filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // нечитаемый узел — не повод обрывать обход
		}
		if path == absRoot {
			return nil
		}
		rel, relErr := filepath.Rel(absRoot, path)
		if relErr != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)

		if workspace.IsIgnored(relSlash) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// Симлинки-файлы пропускаются здесь же, ДО visit, проверкой бита
		// режима уже полученного DirEntry — без единого лишнего syscall
		// (директории WalkDir и так не пересекает: fs.WalkDir не следует
		// symlink-каталогам). Раньше единственной защитой от сбегающих
		// симлинков был SafeJoin ВНУТРИ discoverComponent (EvalSymlinks +
		// inside()) — но discoverComponentMeta (precheck, без SafeJoin — см.
		// её комментарий) такой защиты не имела: сбегающий симлинк никогда
		// не попадает в corpus.files (его исключает discoverComponent),
		// поэтому precheckChangedCount видел бы его как «изменившийся» на
		// КАЖДЫЙ вызов бессрочно (ошибка в консервативную сторону — лишний
		// синхронный инкремент, не stale-как-fresh, но всё равно долг).
		// Фильтр здесь же попутно унифицирует поведение с симлинками ВНУТРИ
		// корня: раньше SafeJoin успешно резолвил их absPath, а
		// relPath/size/mtime всё равно брались из d.Info() (Lstat самой
		// ссылки, не цели) — нигде не документированное полуправило.
		// Исключая любой симлинк одним фильтром для обоих потребителей,
		// discoverComponent и discoverComponentMeta гарантированно видят
		// один и тот же набор файлов.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if !pathIncluded(relSlash, include, exclude) {
			return nil
		}
		return visit(relSlash, rel, d)
	})
}

// discoverComponent обходит корень компонента через workspace.SafeJoin.
//
// Список возвращается отсортированным по relPath: детерминизм входа —
// обязательное условие §18.7.
func discoverComponent(absRoot string, include, exclude []string) ([]discoveredFile, error) {
	var out []discoveredFile
	err := walkComponent(absRoot, include, exclude, func(relSlash, rel string, d fs.DirEntry) error {
		// SafeJoin: единственная точка валидации путей:
		// вычисляет безопасный absPath для downstream-чтения содержимого
		// (fingerprint/parse). Симлинки walkComponent уже отсеял выше — сюда
		// они не доходят, но SafeJoin остаётся ЕДИНСТВЕННЫМ местом, которое
		// умеет резолвить и провалидировать сам путь до файла (не только
		// его тип), поэтому вызов не убран — precheck (discoverComponentMeta)
		// этого не делает, ему absPath не нужен, см. её комментарий.
		safe, safeErr := workspace.SafeJoin(absRoot, rel)
		if safeErr != nil {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		out = append(out, discoveredFile{
			relPath: relSlash,
			absPath: safe,
			size:    info.Size(),
			mtimeNS: info.ModTime().UnixNano(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("обход корня %s: %w", absRoot, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].relPath < out[j].relPath })
	return out, nil
}

// discoverComponentMeta — облегчённая версия discoverComponent для
// precheckChangedCount (§18.5, найдено TestBuildIsolatedLatency/
// realdump_test.go): та же фильтрация через walkComponent, но БЕЗ
// workspace.SafeJoin на файл. precheckChangedCount сравнивает только
// relPath/size/mtimeNS с corpus.files — absPath ему не нужен, файл никогда
// не открывается на этом пути. SafeJoin тут не просто bare os.Stat: это
// filepath.Abs+EvalSymlinks корня (на каждый(!) файл заново) плюс
// EvalSymlinks всего пути файла — на реальной выгрузке ut_demo (48698
// файлов) это ~40% времени всего обхода (измерено: discoverComponent
// 1.57с/EvalSymlinks-часть внутри него ~0.6-0.8с), чистая трата на пути, где
// результат (safe-путь) никогда не читается. os.DirEntry.Info() в WalkDir —
// это Lstat, не резолвит симлинк; поведение сравнения size/mtime от этого
// не меняется — обычный discoverComponent тоже берёт size/mtime из d.Info(),
// не из резолвленного safe-пути. Порядок выдачи не гарантирован (вызывающий
// сравнивает по map, не итерирует по порядку) — сортировка тут была бы
// тратой без потребителя.
//
// ctx проверяется на каждом файле: обход ut_demo идёт секунды, и Close
// сервиса не должен их ждать (ADR-036).
func discoverComponentMeta(ctx context.Context, absRoot string, include, exclude []string) ([]discoveredMeta, error) {
	var out []discoveredMeta
	err := walkComponent(absRoot, include, exclude, func(relSlash, _ string, d fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		out = append(out, discoveredMeta{
			relPath: relSlash,
			size:    info.Size(),
			mtimeNS: info.ModTime().UnixNano(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("обход корня %s: %w", absRoot, err)
	}
	return out, nil
}

// pathIncluded решает судьбу файла по include/exclude компонента
// (workspace.MatchPath): пустой include — включено всё, что не попало под
// exclude; непустой include — включено только совпавшее с ним и не попавшее
// под exclude.
func pathIncluded(rel string, include, exclude []string) bool {
	for _, pat := range exclude {
		if ok, err := workspace.MatchPath(pat, rel); err == nil && ok {
			return false
		}
	}
	if len(include) == 0 {
		return true
	}
	for _, pat := range include {
		if ok, err := workspace.MatchPath(pat, rel); err == nil && ok {
			return true
		}
	}
	return false
}

// hasExt сообщает, оканчивается ли rel на один из перечисленных суффиксов
// без учёта регистра (расширения путей 1С регистронезависимы на Windows).
func hasExt(rel string, exts ...string) bool {
	lower := strings.ToLower(rel)
	for _, e := range exts {
		if strings.HasSuffix(lower, e) {
			return true
		}
	}
	return false
}
