package main

import (
	"strings"
	"testing"
)

func scorePairs() []pair {
	return []pair{
		{ID: "a", Section: sectionBSP, Split: splitDev, Stratum: stratumCore, Variant: variantDirect, NameOverlap: true, Accept: []string{"М.А"}},
		{ID: "b", Section: sectionBSP, Split: splitDev, Stratum: stratumRest, Variant: variantParaphrase, NameOverlap: false, Accept: []string{"М.Б"}},
		{ID: "c", Section: sectionBSP, Split: splitTest, Stratum: stratumRest, Variant: variantDirect, NameOverlap: true, Accept: []string{"М.В"}},
		{ID: "d", Section: sectionOther, Split: splitDev, Stratum: stratumRest, Variant: variantDirect, NameOverlap: true, Accept: []string{"П.Г"}},
		{ID: "e", Section: sectionOther, Split: splitTest, Stratum: stratumRest, Variant: variantDirect, NameOverlap: true, Accept: []string{"П.Д"}},
	}
}

// scoreResults: исходы пяти пар; место до scoreShown включительно значит,
// что метод виден и в выдаче по умолчанию, место глубже видно только в
// длинной выдаче.
func scoreResults(positions ...int) []result {
	ids := []string{"a", "b", "c", "d", "e"}
	out := make([]result, len(positions))
	for i, p := range positions {
		out[i] = result{ID: ids[i], Deep: p, DurationMS: float64(100 * (i + 1))}
		if p <= scoreShown {
			out[i].Shown = p
		}
	}
	return out
}

func scoreReport(t *testing.T, dataset string, positions ...int) report {
	t.Helper()
	groups, median, err := summarize(scorePairs(), scoreResults(positions...))
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	return report{Dataset: dataset, Catalog: catalogInfo{BSPVersion: "3.1", BSP: 3, Other: 2}, Groups: groups, MedianMS: median, Jobs: 1}
}

// TestPositionOf: место считается по первому засчитанному вызову, близнец
// засчитывается наравне с самим методом, регистр имени не важен.
func TestPositionOf(t *testing.T) {
	calls := []string{"М.Чужой", "ОбщегоНазначенияКлиент.Сообщить", "ОбщегоНазначения.Сообщить"}
	accept := []string{"общегоназначения.сообщить", "ОбщегоНазначенияКлиент.Сообщить"}
	if got := positionOf(calls, accept); got != 2 {
		t.Errorf("positionOf = %d, want 2", got)
	}
	if got := positionOf(calls, []string{"М.Нет"}); got != 0 {
		t.Errorf("positionOf ненайденного = %d, want 0", got)
	}
}

// TestBestPosition: метод, найденный в соседней секции, найден; из двух мест
// берётся лучшее.
func TestBestPosition(t *testing.T) {
	bsp := []string{"М.А", "М.Б", "ОбщегоНазначения.Значение"}
	other := []string{"ОбщегоНазначенияУТ.Значение", "П.Г"}
	if got := bestPosition([]string{"П.Г"}, bsp, other); got != 2 {
		t.Errorf("метод из соседней секции: место %d, want 2", got)
	}
	if got := bestPosition([]string{"ОбщегоНазначения.Значение", "ОбщегоНазначенияУТ.Значение"}, bsp, other); got != 1 {
		t.Errorf("лучшее из двух мест = %d, want 1", got)
	}
	if got := bestPosition([]string{"М.Нет"}, bsp, other); got != 0 {
		t.Errorf("ненайденный: место %d, want 0", got)
	}
}

// TestResultHit: первая десятка считается по выдаче по умолчанию, а не по
// месту в длинной выдаче: метод, которого в выдаче по умолчанию нет, в первую
// десятку не засчитывается, даже если в длинной он стоит десятым.
func TestResultHit(t *testing.T) {
	r := result{Shown: 0, Deep: 10}
	if r.hit(scoreShown) {
		t.Errorf("метод вне выдачи по умолчанию засчитан в первые %d", scoreShown)
	}
	if !r.hit(scoreDepth) {
		t.Errorf("метод с местом 10 в длинной выдаче не засчитан в первые %d", scoreDepth)
	}
	r = result{Shown: 4, Deep: 0}
	if !r.hit(scoreShown) || !r.hit(scoreDepth) || r.hit(3) {
		t.Errorf("метод с местом 4 в выдаче по умолчанию: hit(3)=%v hit(10)=%v hit(50)=%v", r.hit(3), r.hit(scoreShown), r.hit(scoreDepth))
	}
}

// TestSummarize: доли считаются по секции, половине, слою, виду запроса и
// признаку общих слов; ненайденный запрос в долю не входит и в MRR даёт ноль.
func TestSummarize(t *testing.T) {
	rep := scoreReport(t, "hash", 1, 0, 4, 12, 3)
	bsp := rep.Groups[sectionBSP]
	if bsp.N != 3 || bsp.Hits[1] != 1 || bsp.Hits[3] != 1 || bsp.Hits[10] != 2 || bsp.Hits[50] != 2 {
		t.Errorf("bsp = %+v", bsp)
	}
	if want := (1.0 + 0 + 0.25) / 3; bsp.MRR < want-1e-9 || bsp.MRR > want+1e-9 {
		t.Errorf("bsp MRR = %v, want %v", bsp.MRR, want)
	}
	if g := rep.Groups[sectionBSP+"/"+splitDev]; g.N != 2 || g.Hits[10] != 1 {
		t.Errorf("bsp/dev = %+v", g)
	}
	if g := rep.Groups[sectionBSP+"/без общих слов с именем"]; g.N != 1 || g.Hits[50] != 0 {
		t.Errorf("bsp без общих слов = %+v", g)
	}
	if g := rep.Groups[sectionBSP+"/слой "+stratumCore]; g.N != 1 || g.Hits[1] != 1 {
		t.Errorf("bsp/слой core = %+v", g)
	}
	if g := rep.Groups[sectionBSP+"/пересказ"]; g.N != 1 || g.Hits[50] != 0 {
		t.Errorf("bsp/пересказ = %+v", g)
	}
	if g := rep.Groups[sectionBSP+"/"+splitDev+"/прямой запрос"]; g.N != 1 || g.Hits[1] != 1 {
		t.Errorf("bsp/dev/прямой запрос = %+v", g)
	}
	if g := rep.Groups[sectionOther]; g.N != 2 || g.Hits[10] != 1 || g.Hits[50] != 2 {
		t.Errorf("other = %+v", g)
	}
	if rep.MedianMS != 300 {
		t.Errorf("MedianMS = %v, want 300", rep.MedianMS)
	}
	if _, _, err := summarize(scorePairs(), scoreResults(1, 0, 4)); err == nil {
		t.Errorf("прогон без исхода по паре принят")
	}
	if text := formatReport(rep); !strings.Contains(text, "bsp/dev") || !strings.Contains(text, "в первых 10") {
		t.Errorf("таблица итога:\n%s", text)
	}
}

// TestCompare: падение на глубине 10 или 50 в любой половине секции валит
// сравнение, перестановка внутри глубины нет; база с другого набора или с
// другой выгрузки не сравнивается.
func TestCompare(t *testing.T) {
	base := scoreReport(t, "hash", 1, 0, 4, 12, 3)

	if drops, err := compare(scoreReport(t, "hash", 9, 0, 1, 50, 10), base); err != nil || len(drops) != 0 {
		t.Errorf("перестановка внутри глубины сочтена падением: %q, %v", drops, err)
	}
	if drops, err := compare(scoreReport(t, "hash", 1, 5, 4, 2, 3), base); err != nil || len(drops) != 0 {
		t.Errorf("улучшение сочтено падением: %q, %v", drops, err)
	}
	drops, err := compare(scoreReport(t, "hash", 1, 0, 11, 12, 3), base)
	if err != nil || len(drops) != 1 || !strings.Contains(drops[0], sectionBSP+"/"+splitTest) || !strings.Contains(drops[0], "первых 10") {
		t.Errorf("падение bsp/test на глубине 10: %q, %v", drops, err)
	}
	if drops, _ := compare(scoreReport(t, "hash", 1, 0, 4, 0, 3), base); len(drops) != 1 || !strings.Contains(drops[0], "первых 50") {
		t.Errorf("пропажа из выдачи other/dev: %q", drops)
	}
	if _, err := compare(scoreReport(t, "other-hash", 1, 0, 4, 12, 3), base); err == nil {
		t.Errorf("база с другого набора принята к сравнению")
	}
	moved := scoreReport(t, "hash", 1, 0, 4, 12, 3)
	moved.Catalog.BSPVersion = "3.2"
	if _, err := compare(moved, base); err == nil {
		t.Errorf("база с другой выгрузки принята к сравнению")
	}
}
