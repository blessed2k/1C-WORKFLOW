package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// check_sync — единственная live-возможность, которая живёт не в HTTPSource, а здесь:
// она сравнивает дерево живой базы с деревом XML-выгрузки. Тест проверяет, что сравнение
// вообще состоялось: имена типов с обеих сторон должны совпасть, иначе всё уедет в
// notComparedTypes и инструмент будет молча отвечать «расхождений нет».
//
// Запуск:
//
//	# учётные данные базы — в переменных окружения MCP_1C_USER/MCP_1C_PASSWORD
//	MCP_1C_BASE_URL=http://localhost:8314/ut/hs/mcp-1c \
//	MCP_1C_DUMP=<каталог выгрузки ut_demo> \
//	    go test ./cmd/mcp1c -run TestLiveCheckSync -v
func TestLiveCheckSync(t *testing.T) {
	base := os.Getenv("MCP_1C_BASE_URL")
	dump := os.Getenv("MCP_1C_DUMP")
	if base == "" || dump == "" {
		t.Skip("нужны MCP_1C_BASE_URL и MCP_1C_DUMP")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	live := source.NewHTTPSource(base, os.Getenv("MCP_1C_USER"), os.Getenv("MCP_1C_PASSWORD"))
	xml := source.NewXMLSource(dump)
	defer xml.Close()

	liveTree, err := live.MetadataTree(ctx)
	if err != nil {
		t.Fatalf("live metadata tree: %v", err)
	}
	dumpTree, err := xml.MetadataTree(ctx)
	if err != nil {
		t.Fatalf("dump metadata tree: %v", err)
	}

	diffs, notCompared := diffMetadataTrees(liveTree, dumpTree)

	// Типы, которые есть только на одной стороне, check_sync не сверяет. Пока живой
	// /metadata отдавал восемь коллекций из двадцати с лишним, сюда попадала почти вся
	// конфигурация, и «расхождений нет» ничего не значило.
	if len(notCompared) > 3 {
		for _, nc := range notCompared {
			t.Logf("не сравнивался тип %s (только %s)", nc.Type, nc.Side)
		}
		t.Errorf("не сравнивалось %d типов, ожидалось не больше трёх", len(notCompared))
	}

	liveOnly := 0
	for _, nc := range notCompared {
		if nc.Side == "live" {
			liveOnly++
		}
	}
	compared := len(liveTree.Groups) - liveOnly
	if compared < 20 {
		t.Errorf("сравнилось всего %d типов, живой /metadata отдаёт мало коллекций", compared)
	}
	t.Logf("сравнилось типов: %d, расхождений по составу: %d, не сравнивалось: %d",
		compared, len(diffs), len(notCompared))
	for _, d := range diffs {
		t.Logf("%s: в базе %d, в выгрузке %d (только в базе %d, только в выгрузке %d)",
			d.Type, d.LiveCount, d.DumpCount, len(d.OnlyInLive), len(d.OnlyInDump))
	}
}
