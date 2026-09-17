package meta

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// dumpRoot возвращает корень реальной выгрузки УТ. Путь берётся только из
// окружения (та же переменная, что у остальных real-dump тестов): хардкодить
// и коммитить его нельзя.
func dumpRoot(t *testing.T) string {
	t.Helper()
	for _, env := range []string{"ONEC_DUMP", "MCP1C_SPIKE_DUMP"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			if _, err := os.Stat(v); err != nil {
				t.Skipf("%s указывает на несуществующий путь: %v", env, err)
			}
			return v
		}
	}
	t.Skip("не задана переменная окружения ONEC_DUMP с путём к реальной выгрузке 1С")
	return ""
}

// TestКорпусВыгрузкиРазбираетсяБезОшибок — критерий приёмки таска: ≥95% XML
// реальной выгрузки разбирается без diagnostic-ошибок, остальное — с честными
// диагностиками. t.Errorf (не t.Logf) красит тест и при падении доли
// успешных файлов, и при обвале числа распознанных фактов.
func TestКорпусВыгрузкиРазбираетсяБезОшибок(t *testing.T) {
	root := dumpRoot(t)

	var totalXML, withErrorDiag int
	var moduleRegistryCount, subscriptionCount, formStructureCount, roleRightsCount, predefinedGroupsCount int
	var subBareKind, subDefinedType, subType int
	var setForNewObjectsTrue int

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // недоступный подкаталог не должен ронять весь прогон
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".xml") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		totalXML++

		facts, diags := ParseFile(rel, src)
		for _, d := range diags {
			if d.Severity == "error" {
				withErrorDiag++
				break
			}
		}

		if facts.ModuleRegistry != nil {
			moduleRegistryCount++
		}
		if facts.Subscription != nil {
			subscriptionCount++
			for _, s := range facts.Subscription.Sources {
				switch s.Kind {
				case SourceKindBareKind:
					subBareKind++
				case SourceKindDefinedType:
					subDefinedType++
				case SourceKindType:
					subType++
				}
			}
		}
		if facts.FormStructure != nil {
			formStructureCount++
		}
		if facts.RoleRights != nil {
			roleRightsCount++
			if facts.RoleRights.SetForNewObjects {
				setForNewObjectsTrue++
			}
		}
		if len(facts.Predefined) > 0 {
			predefinedGroupsCount++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("обход выгрузки: %v", err)
	}
	if totalXML == 0 {
		t.Fatal("в выгрузке не найдено ни одного .xml")
	}

	// --- критерий приёмки: доля файлов без diagnostic-ошибок ---
	okRatio := float64(totalXML-withErrorDiag) / float64(totalXML)
	t.Logf("всего xml=%d, с error-диагностикой=%d, доля успешных=%.4f", totalXML, withErrorDiag, okRatio)
	if okRatio < 0.95 {
		t.Errorf("доля файлов без diagnostic-ошибок %.4f ниже требуемых 0.95 (всего=%d, с ошибками=%d)",
			okRatio, totalXML, withErrorDiag)
	}

	// --- независимые ориентиры: посчитаны обходом файловой системы и сырым
	// поиском подстрок, БЕЗ использования Classify/ParseFile. Если счётчики
	// пакета разошлись с ними — либо факты теряются, либо появляются лишние.
	oracle := independentOracles(t, root)

	if moduleRegistryCount != oracle.commonModuleFiles {
		t.Errorf("ModuleRegistry фактов %d, файлов CommonModules/*.xml (обход ФС) %d — разошлось",
			moduleRegistryCount, oracle.commonModuleFiles)
	}
	if subscriptionCount != oracle.eventSubscriptionFiles {
		t.Errorf("Subscription фактов %d, файлов EventSubscriptions/*.xml (обход ФС) %d — разошлось",
			subscriptionCount, oracle.eventSubscriptionFiles)
	}
	if formStructureCount != oracle.formXMLFiles {
		t.Errorf("FormStructure фактов %d, файлов .../Ext/Form.xml (обход ФС) %d — разошлось",
			formStructureCount, oracle.formXMLFiles)
	}
	if roleRightsCount != oracle.rightsXMLFiles {
		t.Errorf("RoleRights фактов %d, файлов Roles/*/Ext/Rights.xml (обход ФС) %d — разошлось",
			roleRightsCount, oracle.rightsXMLFiles)
	}
	if predefinedGroupsCount != oracle.predefinedXMLFiles {
		t.Errorf("групп Predefined %d, файлов .../Ext/Predefined.xml (обход ФС) %d — разошлось",
			predefinedGroupsCount, oracle.predefinedXMLFiles)
	}
	if setForNewObjectsTrue != oracle.setForNewObjectsTrueFiles {
		t.Errorf("SetForNewObjects=true фактов %d, найдено сырым поиском подстроки %d — разошлось",
			setForNewObjectsTrue, oracle.setForNewObjectsTrueFiles)
	}

	// Критерий R30.1: обе формы источника подписки (голый вид и
	// ОпределяемыйТип) реально встречаются в разборе реальной выгрузки, а не
	// только на придуманной фикстуре — здесь метрика не может быть пустой.
	if subBareKind == 0 {
		t.Error("на реальной выгрузке не нашлось ни одной подписки на голый вид (bare-kind) — R30.1 не подтверждён корпусом")
	}
	if subDefinedType == 0 {
		t.Error("на реальной выгрузке не нашлось ни одной подписки на ОпределяемыйТип — R30.1 не подтверждён корпусом")
	}
	if subBareKind != oracle.subTypeSetNoDefinedType {
		t.Errorf("bare-kind источников %d, независимый подсчёт (TypeSet без DefinedType.) %d — разошлось",
			subBareKind, oracle.subTypeSetNoDefinedType)
	}
	if subDefinedType != oracle.subTypeSetDefinedType {
		t.Errorf("defined-type источников %d, независимый подсчёт (TypeSet c DefinedType.) %d — разошлось",
			subDefinedType, oracle.subTypeSetDefinedType)
	}
	if got, want := subType+subBareKind+subDefinedType, oracle.subSourceTotal; got != want {
		t.Errorf("всего источников подписок %d, независимый подсчёт (файлов с <v8:Type> или <v8:TypeSet>) %d — разошлось", got, want)
	}
}

type oracleCounts struct {
	commonModuleFiles         int
	eventSubscriptionFiles    int
	formXMLFiles              int
	rightsXMLFiles            int
	predefinedXMLFiles        int
	setForNewObjectsTrueFiles int
	subTypeSetNoDefinedType   int
	subTypeSetDefinedType     int
	subSourceTotal            int
}

// independentOracles считает контрольные числа по выгрузке способом, не
// пересекающимся с Classify/ParseFile: прямой обход путей файловой системы и
// поиск байтовых подстрок в сыром содержимом.
func independentOracles(t *testing.T, root string) oracleCounts {
	t.Helper()
	var o oracleCounts

	commonModulesDir := filepath.Join(root, "CommonModules")
	if entries, err := os.ReadDir(commonModulesDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".xml") {
				o.commonModuleFiles++
			}
		}
	}

	// Источников на файл бывает много (некоторые подписки перечисляют сотни
	// конкретных <v8:Type> вместо одного TypeSet), поэтому ориентир считает
	// вхождения элементов, а не факт присутствия подстроки в файле.
	reType := regexp.MustCompile(`<v8:Type>`)
	reTypeSet := regexp.MustCompile(`<v8:TypeSet>([^<]*)</v8:TypeSet>`)

	eventSubsDir := filepath.Join(root, "EventSubscriptions")
	entries, err := os.ReadDir(eventSubsDir)
	if err != nil {
		t.Fatalf("чтение %s: %v", eventSubsDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".xml") {
			continue
		}
		o.eventSubscriptionFiles++
		raw, err := os.ReadFile(filepath.Join(eventSubsDir, e.Name()))
		if err != nil {
			continue
		}
		o.subSourceTotal += len(reType.FindAll(raw, -1))
		for _, m := range reTypeSet.FindAllSubmatch(raw, -1) {
			o.subSourceTotal++
			content := string(m[1])
			if idx := strings.Index(content, ":"); idx >= 0 {
				content = content[idx+1:]
			}
			if strings.HasPrefix(content, "DefinedType.") {
				o.subTypeSetDefinedType++
			} else {
				o.subTypeSetNoDefinedType++
			}
		}
	}

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		switch {
		case strings.HasSuffix(path, string(filepath.Separator)+"Ext"+string(filepath.Separator)+"Form.xml"):
			o.formXMLFiles++
		case strings.HasSuffix(path, string(filepath.Separator)+"Ext"+string(filepath.Separator)+"Predefined.xml"):
			o.predefinedXMLFiles++
		case strings.HasSuffix(path, string(filepath.Separator)+"Ext"+string(filepath.Separator)+"Rights.xml") &&
			strings.Contains(path, string(filepath.Separator)+"Roles"+string(filepath.Separator)):
			o.rightsXMLFiles++
			raw, err := os.ReadFile(path)
			if err == nil && bytes.Contains(raw, []byte("<setForNewObjects>true</setForNewObjects>")) {
				o.setForNewObjectsTrueFiles++
			}
		}
		return nil
	})

	return o
}
