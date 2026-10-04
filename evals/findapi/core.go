package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

// runCore печатает ходовые методы библиотеки этой выгрузки
// (app.APIService.CoreMethods): то, что агент получает вместе с ответом
// get_context_for_task. Показывает размер блока и долю вызовов библиотеки из
// прикладного кода, которую список закрывает.
func runCore(args []string) error {
	fs := flag.NewFlagSet("core", flag.ExitOnError)
	root, workspace, syntaxIndex, reindex := projectFlags(fs)
	fs.Parse(args)

	ctx := context.Background()
	p, err := openProject(ctx, *root, *workspace, *syntaxIndex, *reindex)
	if err != nil {
		return err
	}
	defer p.close()
	// Индекс слов строится первым вызовом поиска: время расчёта списка
	// меряется отдельно от него.
	if _, err := p.api.FindAPI(ctx, app.FindAPIInput{Query: "строка"}); err != nil {
		return err
	}
	t0 := time.Now()
	resp, err := p.api.CoreMethods(ctx)
	if err != nil {
		return err
	}
	took := time.Since(t0)
	item := resp.Items[0]
	chars := 0
	for _, m := range item.Modules {
		line := m.Module + ": " + strings.Join(m.Methods, ", ")
		chars += len([]rune(line)) + 1
		fmt.Println(line)
	}
	fmt.Printf("методов %d в %d модулях, %d знаков; расчёт списка %s\n", item.Methods, len(item.Modules), chars, took.Round(time.Millisecond))
	fmt.Printf("кандидатов (действующих методов программного интерфейса библиотеки, которые зовёт прикладной код, без обработчиков событий): %d;\n", item.Candidates)
	fmt.Printf("список закрывает %.0f%% разрешённых индексом вызовов этих методов из прикладного кода\n", 100*item.Coverage)
	return nil
}
