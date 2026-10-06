package app

import (
	"path/filepath"
	"testing"
)

// TestOtherComponentDirsNamesExtensions: raw-поиск кода ходит по каталогу
// активной выгрузки; остальные компоненты того же проекта (расширения) он
// узнаёт отсюда.
func TestOtherComponentDirsNamesExtensions(t *testing.T) {
	p, op := newExtensionFixtureProject(t, "search-roots", oneExtension())

	dirs := p.OtherComponentDirs()
	if len(dirs) != 1 || dirs[0].ID != "ext" {
		t.Fatalf("OtherComponentDirs = %+v, want один компонент ext", dirs)
	}
	want, err := canonicalRoot(filepath.Join(op.Entry.Root, "ext"))
	if err != nil {
		t.Fatalf("canonicalRoot: %v", err)
	}
	if !equalRootPath(dirs[0].Dir, want) {
		t.Errorf("каталог расширения %q, want %q", dirs[0].Dir, want)
	}
}

// TestOtherComponentDirsEmptyForForeignDump: выгрузка, не описанная ни в одном
// проекте, соседей не имеет: чужие расширения к ней не подмешиваются.
func TestOtherComponentDirsEmptyForForeignDump(t *testing.T) {
	p, _ := newExtensionFixtureProject(t, "search-roots-foreign", oneExtension())
	foreign := t.TempDir()
	writeFile(t, filepath.Join(foreign, "Configuration.xml"), конфигурацияXML("Чужая"))
	p.SetDump(foreign)

	if dirs := p.OtherComponentDirs(); len(dirs) != 0 {
		t.Errorf("OtherComponentDirs = %+v, want пусто", dirs)
	}
}
