package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

const (
	// maxSiteTries: сколько мест вызова метода пробуется, пока не получится
	// годный фрагмент.
	maxSiteTries = 12
	// maxRefPages: сколько страниц find_references читается на метод.
	maxRefPages = 10
	// refPageSize: размер страницы find_references (потолок инструмента).
	refPageSize = 200
)

// tasksOptions: параметры выборки.
type tasksOptions struct {
	Core, Rest, Other int
	Seed, Batch       int
}

func (o tasksOptions) validate() error {
	if o.Core < 0 || o.Rest < 0 || o.Other < 0 {
		return fmt.Errorf("квоты выборки не могут быть отрицательными: bsp-core %d, bsp-rest %d, other %d", o.Core, o.Rest, o.Other)
	}
	if o.Batch <= 0 {
		return fmt.Errorf("размер пачки должен быть положительным: %d", o.Batch)
	}
	return nil
}

// catalog: каталог программного интерфейса, каким он нужен оценке.
type catalog struct {
	info catalogInfo
	// bsp, other: действующие методы секций. Устаревшие в набор не идут:
	// искать их агенту незачем.
	bsp, other []method
	// calls: выражения вызова всех методов, включая устаревшие: то, что
	// find_api способен вернуть.
	calls map[string]bool
	// live: выражения вызова действующих методов, в порядке каталога.
	live []string
}

// readCatalog читает каталог программного интерфейса и раскладывает его по
// секциям.
func readCatalog(ctx context.Context, p *project) (catalog, error) {
	resp, err := p.api.Catalog(ctx)
	if err != nil {
		return catalog{}, err
	}
	for _, w := range resp.Warnings {
		log.Printf("каталог: предупреждение %s: %s", w.Code, w.Message)
	}
	item := resp.Items[0]
	c := catalog{
		info:  catalogInfo{BSPVersion: item.BSPVersion, BSP: len(item.BSP), Other: len(item.Other)},
		calls: map[string]bool{},
	}
	conv := func(section string, items []app.APIMethodItem) []method {
		var out []method
		for _, it := range items {
			c.calls[strings.ToLower(it.Call)] = true
			if it.Deprecated {
				continue
			}
			c.live = append(c.live, it.Call)
			out = append(out, method{Section: section, Call: it.Call, UID: it.UID, Signature: it.Signature, Module: it.Module, Line: it.Line})
		}
		return out
	}
	c.bsp, c.other = conv(sectionBSP, item.BSP), conv(sectionOther, item.Other)
	return c, nil
}

// externalSites: места вызова метода из других модулей. Неоднозначно
// разрешённые ссылки не берутся: по ним неизвестно, тот ли метод позван.
//
// упрощение: читается не больше maxRefPages страниц ссылок (две тысячи
// мест). У самых ходовых методов ссылок больше; их места вызова берутся из
// начала списка в порядке индексации, а число вызовов упирается в потолок.
// Для выбора фрагмента и для слоя «самые вызываемые» этого хватает; точный
// рейтинг потребовал бы счёта вызовов одним запросом в хранилище.
func externalSites(ctx context.Context, p *project, m method) ([]callSite, error) {
	var out []callSite
	cursor := ""
	for page := 0; page < maxRefPages; page++ {
		resp, err := p.graph.FindReferences(ctx, app.FindReferencesInput{UID: m.UID, Kinds: []string{"call"}, Limit: refPageSize, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, g := range resp.Items {
			if g.Module == m.Module {
				continue
			}
			for _, r := range g.References {
				if r.Resolution == "ambiguous" || r.Span.StartLine == 0 {
					continue
				}
				out = append(out, callSite{Module: g.Module, Line: r.Span.StartLine})
			}
		}
		if cursor = resp.NextCursor; cursor == "" {
			break
		}
	}
	return out, nil
}

// taskOf строит задачу по методу: пробует места вызова по порядку, пока из
// одного не получится годный фрагмент.
func taskOf(p *project, pk picked, families, names map[string][]string, seed int) (task, bool) {
	name := methodName(pk.Call)
	for i, site := range siteOrder(pk.method, seed) {
		if i == maxSiteTries {
			break
		}
		src, err := p.readModule(site.Module)
		if err != nil {
			continue
		}
		snippet, ok := buildSnippet(src, site.Line, name)
		if !ok {
			continue
		}
		doc := ""
		if msrc, err := p.readModule(pk.Module); err == nil {
			doc = methodDoc(msrc, pk.Line)
		}
		return task{
			ID: taskID(pk.UID, site), Section: pk.Section, Stratum: pk.Stratum, Call: pk.Call,
			Accept: acceptedCalls(pk.Call, families, names), UID: pk.UID, Signature: pk.Signature, Doc: doc,
			Callers: len(pk.Sites), Site: site, Snippet: snippet,
		}, true
	}
	return task{}, false
}

// buildTasks выбирает методы и готовит по каждому задачу для пишущей модели.
func buildTasks(ctx context.Context, p *project, opt tasksOptions) ([]task, error) {
	cat, err := readCatalog(ctx, p)
	if err != nil {
		return nil, err
	}
	log.Printf("каталог: bsp %d, other %d действующих методов, библиотека %s", len(cat.bsp), len(cat.other), cat.info.BSPVersion)
	// Близнецы и неоднозначные имена считаются по действующим методам:
	// устаревший близнец ответом не считается.
	families, names := familyIndex(cat.live), nameIndex(cat.live)
	ambiguous := ambiguousNames(cat.live)
	unambiguous := func(methods []method) []method {
		var out []method
		for _, m := range methods {
			if !ambiguous[strings.ToLower(methodName(m.Call))] {
				out = append(out, m)
			}
		}
		return out
	}
	bsp, other := unambiguous(cat.bsp), unambiguous(cat.other)
	log.Printf("без методов с неоднозначным именем (%d имён): bsp %d, other %d", len(ambiguous), len(bsp), len(other))

	for i := range bsp {
		if bsp[i].Sites, err = externalSites(ctx, p, bsp[i]); err != nil {
			return nil, fmt.Errorf("места вызова %s: %w", bsp[i].Call, err)
		}
	}
	taken := map[string]bool{}
	var tasks []task
	skipped := 0
	for _, pk := range pickLibrary(bsp, opt.Core, opt.Rest, opt.Seed, taken) {
		if t, ok := taskOf(p, pk, families, names, opt.Seed); ok {
			tasks = append(tasks, t)
		} else {
			skipped++
		}
	}
	log.Printf("bsp: задач %d, методов без годного фрагмента %d", len(tasks), skipped)

	// Прикладных методов на порядок больше, и мест вызова у всех разом никто
	// не спрашивает: методы идут в случайном порядке, пока не наберётся квота.
	got, walked := 0, 0
	for _, m := range shuffled(other, opt.Seed) {
		if got == opt.Other {
			break
		}
		if taken[callFamily(m.Call)] {
			continue
		}
		walked++
		if m.Sites, err = externalSites(ctx, p, m); err != nil {
			return nil, fmt.Errorf("места вызова %s: %w", m.Call, err)
		}
		if len(m.Sites) == 0 {
			continue
		}
		if t, ok := taskOf(p, picked{method: m, Stratum: stratumRest}, families, names, opt.Seed); ok {
			taken[callFamily(m.Call)] = true
			tasks = append(tasks, t)
			got++
		}
	}
	log.Printf("other: задач %d, просмотрено методов %d", got, walked)

	// Задачи перемешиваются, чтобы в одной пачке не шли подряд методы одного
	// модуля.
	sort.SliceStable(tasks, func(i, j int) bool {
		return orderKey(opt.Seed, "task\x00"+tasks[i].UID) < orderKey(opt.Seed, "task\x00"+tasks[j].UID)
	})
	return tasks, nil
}

// batchFiles режет вход модели на пачки: имя файла и содержимое.
func batchFiles[T any](rows []T, batch int) (names []string, bodies [][]byte, err error) {
	if batch <= 0 {
		return nil, nil, fmt.Errorf("размер пачки должен быть положительным: %d", batch)
	}
	for from := 0; from < len(rows); from += batch {
		body, err := encodeJSONL(rows[from:min(from+batch, len(rows))])
		if err != nil {
			return nil, nil, err
		}
		names = append(names, fmt.Sprintf("in-%02d.jsonl", len(names)+1))
		bodies = append(bodies, body)
	}
	return names, bodies, nil
}

// writeBatches кладёт в каталог пачки входа модели и её задание.
//
// Пачки и ответы прежнего запуска рядом с новым входом склеились бы с ним при
// сборке. Поэтому: вход тот же, что уже лежит (повторный запуск на той же
// выгрузке с теми же параметрами), каталог не трогается, и ответы на него
// остаются в силе; вход другой, старые файлы это ошибка, а с clean они
// удаляются.
func writeBatches[T any](dir, prompt string, rows []T, batch int, clean bool) (int, error) {
	names, bodies, err := batchFiles(rows, batch)
	if err != nil {
		return 0, err
	}
	stale := staleFiles(dir)
	if len(stale) > 0 && !sameInputs(dir, names, bodies) {
		if !clean {
			return 0, fmt.Errorf("в %s лежат пачки и ответы прежнего запуска с другим входом (%d файлов, например %s): удалите их флагом -clean или уберите вручную",
				dir, len(stale), filepath.Base(stale[0]))
		}
		for _, name := range stale {
			if err := os.Remove(name); err != nil {
				return 0, err
			}
		}
	}
	for i, name := range names {
		if err := writeText(filepath.Join(dir, name), string(bodies[i])); err != nil {
			return 0, err
		}
	}
	return len(names), writeText(filepath.Join(dir, "PROMPT.md"), prompt)
}

// sameInputs сообщает, совпадает ли вход, лежащий в каталоге, с новым: те же
// файлы пачек с тем же содержимым.
func sameInputs(dir string, names []string, bodies [][]byte) bool {
	existing, _ := filepath.Glob(filepath.Join(dir, "in-*.jsonl"))
	if len(existing) != len(names) {
		return false
	}
	for i, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != string(bodies[i]) {
			return false
		}
	}
	return true
}

// staleFiles возвращает пачки и ответы моделей, оставшиеся в каталоге от
// прежнего запуска.
func staleFiles(dir string) []string {
	var out []string
	for _, pattern := range []string{"in-*.jsonl", "out-*.jsonl"} {
		names, _ := filepath.Glob(filepath.Join(dir, pattern))
		out = append(out, names...)
	}
	sort.Strings(out)
	return out
}
