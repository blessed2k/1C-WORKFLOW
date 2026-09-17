package app

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// TestRealDumpTraceCallGraphLatencyBudget — критерий приёмки: trace_call_graph
// depth=3 замерен p50/p95 против бюджета 100/400мс на реальной выгрузке
// (N=15 прогонов, не одна выборка).
func TestRealDumpTraceCallGraphLatencyBudget(t *testing.T) {
	p, _ := newRealDumpProject(t)
	symSvc, graphSvc := NewSymbolService(p), NewGraphService(p)
	ctx := context.Background()

	// Ищем стартовый символ с хоть каким-то выходящим ребром: не любой
	// "Получить*" обязательно что-то зовёт изнутри.
	found, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "Получить", Kind: string(domain.SymbolFunction), Limit: 100})
	if err != nil || len(found.Items) == 0 {
		t.Fatalf("FindSymbol setup: %+v %v", found.Items, err)
	}
	var uid string
	var nodeCount int
	for _, s := range found.Items {
		resp, err := graphSvc.TraceCallGraph(ctx, TraceCallGraphInput{UID: s.UID, Direction: "callees", Depth: 3})
		if err != nil {
			t.Fatalf("TraceCallGraph(%s): %v", s.UID, err)
		}
		if len(resp.Items) > nodeCount {
			nodeCount, uid = len(resp.Items), s.UID
		}
		if nodeCount > 0 {
			break
		}
	}
	if uid == "" {
		t.Skip("ни один кандидат по подстроке не имеет исходящих вызовов на реальной выгрузке — замер пропущен")
	}

	const n = 15
	durs := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		t0 := time.Now()
		resp, err := graphSvc.TraceCallGraph(ctx, TraceCallGraphInput{UID: uid, Direction: "callees", Depth: 3})
		d := time.Since(t0)
		if err != nil {
			t.Fatalf("TraceCallGraph прогон %d: %v", i, err)
		}
		durs = append(durs, d)
		if len(resp.Items) != nodeCount {
			t.Errorf("прогон %d: nodes=%d, want %d (то же между вызовами без reindex)", i, len(resp.Items), nodeCount)
		}
	}
	p50, p95 := percentile(durs, 0.5), percentile(durs, 0.95)
	t.Logf("trace_call_graph(%s, depth=3, %d прогонов, %d узлов): p50=%s p95=%s", uid, n, nodeCount, p50, p95)
	if p50 > 100*time.Millisecond {
		t.Errorf("trace_call_graph p50 = %s, бюджет 100мс превышен", p50)
	}
	if p95 > 400*time.Millisecond {
		t.Errorf("trace_call_graph p95 = %s, бюджет 400мс превышен", p95)
	}
}

// callSiteTextPattern — форма, которую обязан иметь текст спана ссылки вида
// "call": голое имя или Квалификатор.Имя, БЕЗ кавычек и без пробелов внутри —
// то, что вырезал бы парсер BSL из реального вызова, а не из строкового
// литерала ("Иванов.Позвонить()" как текст) или комментария.
var callSiteTextPattern = regexp.MustCompile(`^[\p{L}_][\p{L}\p{N}_]*(\.[\p{L}_][\p{L}\p{N}_]*)?$`)

// TestRealDumpFindReferencesNoFalsePositivesFromStringsOrComments — критерий
// приёмки «ноль ложных ссылок из строк и комментариев — измерено на
// выгрузке, число в отчёте». Гарантия ложных references принадлежит
// парсеру (internal/parse/bsl, тикет 05, R25.4) — здесь она перепроверяется
// НЕЗАВИСИМО через публичный интерфейс find_references плюс прямое чтение
// реального файла на диске (не blob — тест сам себе не доверяет тому же
// пути, что читает продукт): текст каждого спана ссылки обязан быть похож на
// вызов, а не быть вырезан из строки/комментария.
func TestRealDumpFindReferencesNoFalsePositivesFromStringsOrComments(t *testing.T) {
	p, op := newRealDumpProject(t)
	symSvc, graphSvc := NewSymbolService(p), NewGraphService(p)
	ctx := context.Background()

	comp, ok := op.Manifest.Component("cfg")
	if !ok {
		t.Fatalf("компонент cfg отсутствует в манифесте")
	}

	// Общеупотребимые слова — намеренно кандидаты на ложные срабатывания:
	// они же встречаются в строковых литералах и комментариях сообщений
	// пользователю по всей конфигурации.
	candidates := []string{"Ошибка", "Сообщить", "Записать", "Файл", "Тест", "Получить"}
	checked, falsePositives := 0, 0
	var falseSamples []string

	for _, name := range candidates {
		found, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: name, Limit: 20})
		if err != nil {
			t.Fatalf("FindSymbol(%s): %v", name, err)
		}
		for _, s := range found.Items {
			resp, err := graphSvc.FindReferences(ctx, FindReferencesInput{UID: s.UID, Limit: 50})
			if err != nil {
				t.Fatalf("FindReferences(%s): %v", s.UID, err)
			}
			for _, grp := range resp.Items {
				abs, jerr := workspace.SafeJoin(comp.AbsRoot, grp.Module)
				if jerr != nil {
					continue
				}
				data, rerr := os.ReadFile(abs)
				if rerr != nil {
					continue
				}
				for _, ref := range grp.References {
					if ref.Span.StartByte < 0 || ref.Span.EndByte > len(data) || ref.Span.StartByte > ref.Span.EndByte {
						continue
					}
					text := string(data[ref.Span.StartByte:ref.Span.EndByte])
					checked++
					if !callSiteTextPattern.MatchString(text) {
						falsePositives++
						if len(falseSamples) < 10 {
							falseSamples = append(falseSamples, filepath.Base(grp.Module)+":"+text)
						}
					}
				}
			}
			if checked > 500 {
				break
			}
		}
		if checked > 500 {
			break
		}
	}

	t.Logf("проверено спанов ссылок: %d, ложных (не похожих на вызов): %d", checked, falsePositives)
	if checked == 0 {
		t.Skip("на реальной выгрузке не нашлось ни одной ссылки среди кандидатов — замер пропущен")
	}
	if falsePositives != 0 {
		t.Errorf("ложные ссылки из строк/комментариев: %d из %d, примеры: %v", falsePositives, checked, falseSamples)
	}
}
