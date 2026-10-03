package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

// Замер обратной проверки черновика (app.APIService.ReadyMethods, поле
// readyMethods у validate_bsl). Вопрос к ней другой, чем к find_api: агент не
// спросил, а написал свою функцию; называет ли проверка готовый метод, который
// эта функция повторяет, и сколько лишнего называет на функциях, которые
// ничего не повторяют.
//
// Повторы: файл черновиков. По запросу пары эталона модель вслепую написала
// заголовок «своей» функции (имя и строка комментария), не зная готового
// метода. Повтор найден, когда среди названных кандидатов есть засчитываемый
// метод пары.
//
// Шум: служебные (неэкспортные) функции модулей форм и модулей объектов самой
// выгрузки. Готовых методов они, как правило, не повторяют, и почти всё
// названное для них лишнее. Берутся прямо из выгрузки при прогоне, в
// репозиторий не попадают.

// draft: заголовок функции, написанной вместо готового метода пары ID.
type draft struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Comment string `json:"comment"`
}

// draftCode: черновик из одной функции, как его получит validate_bsl.
func draftCode(name, comment string) string {
	var b strings.Builder
	if comment != "" {
		b.WriteString("// " + comment + "\n")
	}
	b.WriteString("Функция " + name + "()\nКонецФункции\n")
	return b.String()
}

// draftOutcome: что обратная проверка сказала о функции.
type draftOutcome struct {
	// checked: функция проверялась (не пропущена как обработчик и не как
	// имя из одного слова).
	checked bool
	// calls: названные готовые методы.
	calls []string
}

func readyFor(ctx context.Context, p *project, name, comment string) (draftOutcome, error) {
	resp, err := p.api.ReadyMethods(ctx, app.ReadyMethodsInput{Code: draftCode(name, comment)})
	if err != nil {
		return draftOutcome{}, err
	}
	rep := resp.Items[0]
	out := draftOutcome{checked: rep.Checked > 0}
	for _, m := range rep.Methods {
		for _, c := range m.Candidates {
			out.calls = append(out.calls, c.Call)
		}
	}
	return out, nil
}

// draftStats: итог по группе черновиков.
type draftStats struct {
	n, found, foundByName, skipped int
}

func runDrafts(args []string) error {
	fs := flag.NewFlagSet("drafts", flag.ExitOnError)
	root, workspace, syntaxIndex, reindex := projectFlags(fs)
	data := fs.String("data", "", "файл набора (обязателен)")
	drafts := fs.String("drafts", "", "файл черновиков: id пары, name, comment (обязателен)")
	noise := fs.Int("noise", 1000, "сколько служебных функций выгрузки проверить на шум; 0: не проверять")
	fs.Parse(args)
	if *data == "" || *drafts == "" {
		return fmt.Errorf("drafts: флаги -data и -drafts обязательны")
	}
	pairs, err := readJSONLFile[pair](*data)
	if err != nil {
		return err
	}
	rows, err := readJSONLFile[draft](*drafts)
	if err != nil {
		return err
	}
	byID := map[string]pair{}
	for _, p := range pairs {
		byID[p.ID] = p
	}

	ctx := context.Background()
	p, err := openProject(ctx, *root, *workspace, *syntaxIndex, *reindex)
	if err != nil {
		return err
	}
	defer p.close()
	if err := checkCards(ctx, p); err != nil {
		return err
	}

	stats := map[string]*draftStats{}
	at := func(key string) *draftStats {
		if stats[key] == nil {
			stats[key] = &draftStats{}
		}
		return stats[key]
	}
	for _, d := range rows {
		pr, ok := byID[d.ID]
		if !ok {
			return fmt.Errorf("черновик %s: такой пары в наборе нет", d.ID)
		}
		full, err := readyFor(ctx, p, d.Name, d.Comment)
		if err != nil {
			return fmt.Errorf("черновик %s: %w", d.ID, err)
		}
		bare, err := readyFor(ctx, p, d.Name, "")
		if err != nil {
			return fmt.Errorf("черновик %s: %w", d.ID, err)
		}
		for _, key := range []string{pr.Section + "/" + pr.Split, pr.Section, "все/" + pr.Split, "все"} {
			s := at(key)
			s.n++
			if !full.checked {
				s.skipped++
			}
			if positionOf(full.calls, pr.Accept) > 0 {
				s.found++
			}
			if positionOf(bare.calls, pr.Accept) > 0 {
				s.foundByName++
			}
		}
	}
	cards, err := cardsInfo()
	if err != nil {
		return err
	}
	if cards == "" {
		cards = "нет"
	}
	fmt.Printf("карточки поиска: %s\n", cards)
	fmt.Printf("%-12s %9s %24s %24s %10s\n", "группа", "функций", "метод назван", "назван по одному имени", "пропущено")
	keys := make([]string, 0, len(stats))
	for k := range stats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := stats[k]
		fmt.Printf("%-12s %9d %17d (%3.0f%%) %17d (%3.0f%%) %10d\n", k, s.n,
			s.found, 100*float64(s.found)/float64(s.n), s.foundByName, 100*float64(s.foundByName)/float64(s.n), s.skipped)
	}

	if *noise == 0 {
		return nil
	}
	local, err := localFunctions(p.root, *noise)
	if err != nil {
		return err
	}
	checked, named, lines := 0, 0, 0
	for _, f := range local {
		out, err := readyFor(ctx, p, f.Name, f.Comment)
		if err != nil {
			return fmt.Errorf("служебная функция %s: %w", f.Name, err)
		}
		if !out.checked {
			continue
		}
		checked++
		if len(out.calls) > 0 {
			named++
		}
		lines += len(out.calls)
	}
	if checked == 0 {
		return fmt.Errorf("в выгрузке не нашлось служебных функций для замера шума")
	}
	fmt.Printf("шум: служебных функций форм и модулей объектов %d, проверялось %d (остальные пропущены как обработчики и имена из одного слова);\n", len(local), checked)
	fmt.Printf("     кандидаты названы у %d (%.0f%%), строк на проверенную функцию %.2f\n", named, 100*float64(named)/float64(checked), float64(lines)/float64(checked))
	return nil
}

// reLocalFunction: объявление неэкспортной процедуры или функции.
var reLocalFunction = regexp.MustCompile(`(?im)^[ \t]*(?:асинх[ \t]+)?(?:процедура|функция)[ \t]+([\p{L}_][\p{L}\p{N}_]*)[ \t]*\(([^\n]*)$`)

// localFunctions берёт из выгрузки служебные функции модулей форм и модулей
// объектов: до limit штук, в порядке, который задаёт хэш пути и имени (один и
// тот же на каждом прогоне).
func localFunctions(root string, limit int) ([]draft, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".bsl") {
			return nil
		}
		rel := filepath.ToSlash(path)
		if strings.Contains(rel, "/Forms/") || strings.EqualFold(filepath.Base(path), "ObjectModule.bsl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	order := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	}
	rel := func(path string) string {
		r, err := filepath.Rel(root, path)
		if err != nil {
			return path
		}
		return filepath.ToSlash(r)
	}
	sort.Slice(files, func(i, j int) bool { return order(rel(files[i])) < order(rel(files[j])) })
	// Файлов читается с запасом: в модуле формы служебных функций несколько.
	if len(files) > limit {
		files = files[:limit]
	}
	type keyed struct {
		key string
		d   draft
	}
	var all []keyed
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		text := strings.TrimPrefix(string(raw), string(rune(0xFEFF)))
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			m := reLocalFunction.FindStringSubmatch(line)
			if m == nil || strings.Contains(strings.ToLower(m[2]), "экспорт") {
				continue
			}
			all = append(all, keyed{key: order(rel(path) + "::" + m[1]), d: draft{Name: m[1], Comment: commentAbove(lines, i)}})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].key < all[j].key })
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]draft, len(all))
	for i, k := range all {
		out[i] = k.d
	}
	return out, nil
}

// commentAbove: первая содержательная строка комментария вплотную над
// объявлением (директивы между ними пропускаются).
func commentAbove(lines []string, head int) string {
	end := head
	for end > 0 && strings.HasPrefix(strings.TrimSpace(lines[end-1]), "&") {
		end--
	}
	start := end
	for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "//") {
		start--
	}
	for _, l := range lines[start:end] {
		if text := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "/")); text != "" {
			return text
		}
	}
	return ""
}

// readJSONLFile читает файл JSONL целиком.
func readJSONLFile[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return decodeJSONL[T](f, path)
}
