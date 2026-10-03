package main

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

// runTypes считает, у какой доли функций программного интерфейса find_api
// достаёт тип результата из комментария: у всех функций и у тех, в чьём
// комментарии есть раздел «Возвращаемое значение». Печатает и самые частые
// типы: по ним видно, не принимает ли разбор за тип слово описания.
func runTypes(args []string) error {
	fs := flag.NewFlagSet("types", flag.ExitOnError)
	root, workspace, syntaxIndex, reindex := projectFlags(fs)
	top := fs.Int("top", 15, "сколько самых частых типов результата напечатать")
	fs.Parse(args)

	ctx := context.Background()
	p, err := openProject(ctx, *root, *workspace, *syntaxIndex, *reindex)
	if err != nil {
		return err
	}
	defer p.close()
	resp, err := p.api.Catalog(ctx)
	if err != nil {
		return err
	}
	item := resp.Items[0]
	freq := map[string]int{}
	for _, section := range []struct {
		name  string
		items []app.APIMethodItem
	}{{sectionBSP, item.BSP}, {sectionOther, item.Other}} {
		functions, withSection, typed := 0, 0, 0
		for _, m := range section.items {
			if m.Deprecated || m.Kind != "function" {
				continue
			}
			functions++
			if !strings.Contains(strings.ToLower(m.Doc), "возвращаемое значение") {
				continue
			}
			withSection++
			if m.Returns != "" {
				typed++
				freq[m.Returns]++
			}
		}
		if functions == 0 {
			continue
		}
		fmt.Printf("%s: действующих функций %d, с разделом «Возвращаемое значение» %d, тип результата разобран у %d (%.0f%% от раздела, %.0f%% от всех функций)\n",
			section.name, functions, withSection, typed, 100*float64(typed)/float64(max(withSection, 1)), 100*float64(typed)/float64(functions))
	}
	names := make([]string, 0, len(freq))
	for name := range freq {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if freq[names[i]] != freq[names[j]] {
			return freq[names[i]] > freq[names[j]]
		}
		return names[i] < names[j]
	})
	fmt.Println("самые частые типы результата:")
	for _, name := range names[:min(*top, len(names))] {
		fmt.Printf("  %5d  %s\n", freq[name], name)
	}
	return nil
}
