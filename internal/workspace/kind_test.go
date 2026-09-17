package workspace_test

import (
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// TestDetectKind закрывает D01: internal/workspace/kind.go больше не разбирает
// XML сам через encoding/xml, а делегирует meta.DetectRoot. Тест доказывает
// это не заглядыванием в исходники (grep неуместен как Go-тест), а тем, что
// DetectKind даёт те же результаты на тех же фикстурах, что и раньше —
// ожидания здесь разобраны вручную по содержимому фикстуры, а не взяты из
// вывода самого кода. Фикстуры (конфигурацияXML, внешнийОбъектXML, записать)
// общие с manifest_test.go — тот же пакет workspace_test.
func TestDetectKind(t *testing.T) {
	t.Run("конфигурация", func(t *testing.T) {
		root := t.TempDir()
		записать(t, filepath.Join(root, "Configuration.xml"), конфигурацияXML("УправлениеТорговлей", ""))

		got, err := workspace.DetectKind(root)
		if err != nil {
			t.Fatalf("DetectKind: %v", err)
		}
		want := workspace.DetectedKind{Kind: domain.KindConfiguration, Name: "УправлениеТорговлей", ExtensionPurpose: ""}
		if got != want {
			t.Errorf("DetectKind = %+v, хотели %+v", got, want)
		}
	})

	t.Run("расширение по непустому ConfigurationExtensionPurpose", func(t *testing.T) {
		root := t.TempDir()
		записать(t, filepath.Join(root, "Configuration.xml"), конфигурацияXML("ИсправлениеОшибок", "Patch"))

		got, err := workspace.DetectKind(root)
		if err != nil {
			t.Fatalf("DetectKind: %v", err)
		}
		want := workspace.DetectedKind{Kind: domain.KindExtension, Name: "ИсправлениеОшибок", ExtensionPurpose: "Patch"}
		if got != want {
			t.Errorf("DetectKind = %+v, хотели %+v", got, want)
		}
	})

	t.Run("внешняя обработка по корневому XML", func(t *testing.T) {
		root := t.TempDir()
		записать(t, filepath.Join(root, "ЗагрузкаЦен.xml"), внешнийОбъектXML("ExternalDataProcessor", "ЗагрузкаЦен"))

		got, err := workspace.DetectKind(root)
		if err != nil {
			t.Fatalf("DetectKind: %v", err)
		}
		want := workspace.DetectedKind{Kind: domain.KindExternalDataProcessor, Name: "ЗагрузкаЦен", ExtensionPurpose: ""}
		if got != want {
			t.Errorf("DetectKind = %+v, хотели %+v", got, want)
		}
	})

	t.Run("внешний отчёт по корневому XML", func(t *testing.T) {
		root := t.TempDir()
		записать(t, filepath.Join(root, "СверкаОстатков.xml"), внешнийОбъектXML("ExternalReport", "СверкаОстатков"))

		got, err := workspace.DetectKind(root)
		if err != nil {
			t.Fatalf("DetectKind: %v", err)
		}
		want := workspace.DetectedKind{Kind: domain.KindExternalReport, Name: "СверкаОстатков", ExtensionPurpose: ""}
		if got != want {
			t.Errorf("DetectKind = %+v, хотели %+v", got, want)
		}
	})

	t.Run("ни Configuration.xml, ни корневого XML — ошибка", func(t *testing.T) {
		root := t.TempDir()
		записать(t, filepath.Join(root, "СлучайныйФайл.txt"), "не xml")

		if _, err := workspace.DetectKind(root); err == nil {
			t.Fatal("хотели ошибку на каталоге без распознаваемого XML")
		}
	})
}
