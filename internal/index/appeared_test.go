package index

import (
	"fmt"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// appearedCorpus: newObjects документов, чей XML появился в инкременте (у
// каждого модуль объекта, модуль менеджера и форма), и oldObjects документов,
// которые уже были в индексе, с теми же файлами. Имена "Док1" и "Док10"
// проверяют, что каталог сверяется целиком, а не префиксом имени.
func appearedCorpus(newObjects, oldObjects int) (*componentCorpus, map[string]bool, map[string]*fileRecord) {
	corpus := newComponentCorpus("cfg")
	changed := map[string]bool{}
	old := map[string]*fileRecord{}
	add := func(name string, appeared bool) {
		xml := workspace.DumpDeclarationPath("Document", name)
		corpus.files[xml] = &fileRecord{relPath: xml, metaFacts: meta.Facts{Object: &meta.MetadataObjectFact{}}}
		changed[xml] = true
		if !appeared {
			old[xml] = corpus.files[xml]
		}
		for _, rel := range []string{
			workspace.DumpModulePath("Document", name, workspace.ModuleObject),
			workspace.DumpModulePath("Document", name, workspace.ModuleManager),
			workspace.DumpFormModulePath("Document", name, "ФормаДокумента"),
		} {
			corpus.files[rel] = &fileRecord{relPath: rel}
		}
	}
	for i := 0; i < newObjects; i++ {
		add(fmt.Sprintf("Док%d", i), true)
	}
	for i := 0; i < oldObjects; i++ {
		add(fmt.Sprintf("Старый%d", i), false)
	}
	return corpus, changed, old
}

// TestFilesOfAppearedObjects: переопубликуются ровно файлы каталогов
// появившихся объектов, без их XML и без файлов уже бывших объектов. 1000
// новых объектов на корпусе в 20 тысяч файлов: вложенный перебор «корпус на
// каталоги» дал бы 2*10^7 сравнений, проход по префиксам пути линеен.
func TestFilesOfAppearedObjects(t *testing.T) {
	corpus, changed, old := appearedCorpus(1000, 4000)
	got := filesOfAppearedObjects(corpus, changed, old)
	if len(got) != 3000 {
		t.Fatalf("файлов %d, ожидалось 3000 (по три на каждый из 1000 новых объектов)", len(got))
	}
	seen := map[string]bool{}
	for _, rel := range got {
		seen[rel] = true
	}
	for _, name := range []string{"Док1", "Док10", "Док999"} {
		rel := workspace.DumpModulePath("Document", name, workspace.ModuleObject)
		if !seen[rel] {
			t.Errorf("нет %s", rel)
		}
	}
	for _, rel := range []string{
		workspace.DumpDeclarationPath("Document", "Док1"),
		workspace.DumpModulePath("Document", "Старый1", workspace.ModuleObject),
	} {
		if seen[rel] {
			t.Errorf("лишний %s", rel)
		}
	}
}

func BenchmarkFilesOfAppearedObjects(b *testing.B) {
	corpus, changed, old := appearedCorpus(1000, 4000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		filesOfAppearedObjects(corpus, changed, old)
	}
}
