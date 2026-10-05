package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// TestDumpModuleKindFileNames — имя файла модуля по виду. Значения взяты из
// раскладки платформы (DumpConfigToFiles), а не из кода под тестом: вид
// модуля стал параметром правила именно затем, чтобы менеджерский модуль не
// приклеивали литералом у вызывающего, и подмена значения обязана краснеть
// здесь, а не всплывать в чужой фикстуре.
func TestDumpModuleKindFileNames(t *testing.T) {
	cases := []struct {
		kind ModuleKind
		want string
	}{
		{ModuleObject, "Documents/Заказ/Ext/ObjectModule.bsl"},
		{ModuleManager, "Documents/Заказ/Ext/ManagerModule.bsl"},
		{ModuleRecordSet, "Documents/Заказ/Ext/RecordSetModule.bsl"},
		{ModuleValueManager, "Documents/Заказ/Ext/ValueManagerModule.bsl"},
		{ModuleCommand, "Documents/Заказ/Ext/CommandModule.bsl"},
		{ModuleCommon, "Documents/Заказ/Ext/Module.bsl"},
	}
	for _, c := range cases {
		if got := DumpModulePath("Document", "Заказ", c.kind); got != c.want {
			t.Errorf("DumpModulePath(вид %q) = %q, want %q", c.kind, got, c.want)
		}
	}
}

// TestDumpChildSubsystemPath — путь вложенной подсистемы. Значения взяты из
// раскладки платформы (так лежат подсистемы БСП в выгрузке типовой), а не из
// кода под тестом: читатель состава подсистемы и его фикстура строят путь
// одной функцией, и ошибка в ней иначе была бы согласована сама с собой.
func TestDumpChildSubsystemPath(t *testing.T) {
	cases := []struct {
		parent, child, want string
	}{
		{"Subsystems/СтандартныеПодсистемы.xml", "БазоваяФункциональность",
			"Subsystems/СтандартныеПодсистемы/Subsystems/БазоваяФункциональность.xml"},
		{"Subsystems/А/Subsystems/Б.xml", "В", "Subsystems/А/Subsystems/Б/Subsystems/В.xml"},
	}
	for _, c := range cases {
		if got := DumpChildSubsystemPath(c.parent, c.child); got != c.want {
			t.Errorf("DumpChildSubsystemPath(%q, %q) = %q, want %q", c.parent, c.child, got, c.want)
		}
	}
	if got, want := DumpDeclarationPath("Subsystem", "СтандартныеПодсистемы"), cases[0].parent; got != want {
		t.Errorf("объявление подсистемы верхнего уровня = %q, want %q", got, want)
	}
}

// TestEveryCollectionIsConformant — построение и проверка формы обязаны знать
// один и тот же список. Иначе правило расходится само с собой: путь построен,
// а проверка формы его не признаёт — и гард выдаёт автору сида ложную тревогу.
func TestEveryCollectionIsConformant(t *testing.T) {
	for _, k := range domain.MetaKinds() {
		mtype, dir := k.MType, k.DumpDir
		got, ok := DumpCollectionDir(mtype)
		if !ok || got != dir {
			t.Errorf("DumpCollectionDir(%q) = %q,%v, want %q,true", mtype, got, ok, dir)
		}
		for _, path := range []string{
			DumpDeclarationPath(mtype, "Объект"),
			DumpModulePath(mtype, "Объект", ModuleObject),
			DumpFormModulePath(mtype, "Объект", "ФормаЭлемента"),
		} {
			if !IsDumpConformantPath(path) {
				t.Errorf("IsDumpConformantPath(%q) = false для вида %q", path, mtype)
			}
		}
	}
}

// TestUnknownMTypeIsRefused — неописанный вид объекта обязан быть ЯВНЫМ
// отказом. Прежнее наивное множественное число молча производило путь,
// которого не бывает: у FilterCriterion оно давало "FilterCriterions", и
// собственная же проверка формы такой путь отвергала — автор сида получал
// совет сделать то, что он уже сделал.
func TestUnknownMTypeIsRefused(t *testing.T) {
	if _, ok := DumpCollectionDir("ВыдуманныйВид"); ok {
		t.Fatalf("DumpCollectionDir на неописанном виде отвечает ok")
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("DumpObjectDir на неописанном виде не отказал — значит снова угадывает")
		}
		if !strings.Contains(fmtString(r), "ВыдуманныйВид") {
			t.Errorf("отказ не называет вид: %v", r)
		}
	}()
	_ = DumpObjectDir("ВыдуманныйВид", "Объект")
}

func fmtString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if e, ok := v.(error); ok {
		return e.Error()
	}
	return ""
}

// TestDumpCollectionsCoverRealDump — карта коллекций обязана быть НЕ УЖЕ
// выгрузки, из которой правило выведено. Проверяется на реальной выгрузке:
// каждый каталог верхнего уровня, кроме служебного Ext, обязан быть известной
// коллекцией. Иначе гард на фикстурах отвергает пути видов, которые в
// конфигурации есть на самом деле.
//
// Пропуск ровно один: выгрузки нет (ONEC_DUMP не задан).
func TestDumpCollectionsCoverRealDump(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("ONEC_DUMP"))
	if root == "" {
		root = strings.TrimSpace(os.Getenv("MCP1C_SPIKE_DUMP"))
	}
	if root == "" {
		t.Skip("ONEC_DUMP/MCP1C_SPIKE_DUMP не заданы — сверка с реальной выгрузкой пропущена")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Skipf("выгрузка %s не читается: %v", root, err)
	}
	// Ext — служебный каталог конфигурации (модуль управляемого приложения и
	// прочее), коллекцией объектов он не является.
	notCollection := map[string]bool{"Ext": true}
	var unknown []string
	var seen int
	for _, e := range entries {
		if !e.IsDir() || notCollection[e.Name()] {
			continue
		}
		seen++
		if _, known := domain.MetaKindByDumpDir(e.Name()); !known {
			unknown = append(unknown, e.Name())
		}
	}
	if seen == 0 {
		t.Fatalf("в %s нет ни одного каталога коллекции — это не выгрузка конфигурации", filepath.Clean(root))
	}
	if len(unknown) > 0 {
		t.Errorf("каталоги выгрузки, которых нет в domain.MetaKinds: %v. Карта уже выгрузки, гард будет отвергать реальные пути", unknown)
	}
}
