package index

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEmptyParseDiagnosticIgnoresDirectiveOnlyFiles — ревью нашло 887
// ложных срабатываний index_empty_parse на ut_demo: файл из одного BOM и
// оболочки #Если/#Область без кода внутри ни одной ветки — легитимно пустой
// модуль (RecordSetModule.bsl без переопределений — типовой случай), не
// сбой парсера.
func TestEmptyParseDiagnosticIgnoresDirectiveOnlyFiles(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool // ожидаем ли диагностику index_empty_parse
	}{
		{
			name: "только директивы препроцессора — не диагностика",
			data: "#Если Сервер Тогда\n#КонецЕсли\n",
			want: false,
		},
		{
			name: "BOM плюс директивы — не диагностика",
			data: "\xEF\xBB\xBF#Область Открытая\n#КонецОбласти\n",
			want: false,
		},
		{
			name: "пустой файл — не диагностика (нечего парсить)",
			data: "",
			want: false,
		},
		{
			name: "реальный код внутри директив — диагностика, если парсер его не увидел",
			data: "#Если Сервер Тогда\nПроцедураНеЗамеченнаяПарсером\n#КонецЕсли\n",
			want: true,
		},
		{
			name: "только //-комментарии — точки врезки БСП — не диагностика",
			data: "//++ Локализация\n//-- Локализация\n",
			want: false,
		},
		{
			name: "BOM плюс //-комментарии — не диагностика",
			data: "\xEF\xBB\xBF// просто комментарий\n",
			want: false,
		},
		{
			name: "смешанные # и // строки без кода — не диагностика",
			data: "#Если Сервер Тогда\n//++ Локализация\n#КонецЕсли\n//-- Локализация\n",
			want: false,
		},
		{
			name: "реальный код после //-комментария — диагностика, если парсер его не увидел",
			data: "// комментарий\nПроцедураНеЗамеченнаяПарсером\n",
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := parseOneFile("Catalogs/X/Ext/RecordSetModule.bsl", []byte(tc.data))
			got := false
			for _, d := range rec.diagnostics {
				if d.Code == "index_empty_parse" {
					got = true
				}
			}
			if got != tc.want {
				t.Errorf("index_empty_parse = %v, want %v (diagnostics=%v)", got, tc.want, rec.diagnostics)
			}
		})
	}
}

// TestEmptyParseDiagnosticIgnoresRealLocalizationMarkerModule — P8, круг 3:
// реальный модуль ut_demo, состоящий только из точек врезки БСП
// ("//++ Локализация" / "//-- Локализация" внутри #Область/#КонецОбласти),
// не должен ложно триггерить index_empty_parse — распространённейший
// паттерн в реальных конфигурациях, не edge case (в отличие от таска 09,
// где закрыт только BOM/директивный случай). Пропускается без ONEC_DUMP.
func TestEmptyParseDiagnosticIgnoresRealLocalizationMarkerModule(t *testing.T) {
	root := realDumpRoot(t)
	relPath := filepath.Join("CommonModules", "ДоговорыМеждуОрганизациямиЛокализацияКлиентСервер", "Ext", "Module.bsl")
	path := filepath.Join(root, relPath)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("файл не найден в этой выгрузке: %v", err)
	}

	rec := parseOneFile(filepath.ToSlash(relPath), data)
	for _, d := range rec.diagnostics {
		if d.Code == "index_empty_parse" {
			t.Errorf("index_empty_parse ложно сработал на модуле только из //-точек врезки БСП: %s", d.Message)
		}
	}
}
