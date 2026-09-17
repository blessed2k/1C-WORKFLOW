package resolve

import (
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Словари этого файла НЕ разбирают BSL и НЕ разбирают текст запроса — они
// сшивают уже существующие словари идентичности объекта метаданных, которыми
// говорят разные источники фактов:
//
//   - internal/parse/bsl.ManagerRef.MetaType и .RegisterAccess.MetaType —
//     каталог выгрузки во множественном числе на английском (Catalogs,
//     InformationRegisters, ...), словарь видов общий: domain.MetaKinds;
//   - internal/parse/meta.MetadataObjectFact.MType — имя корневого
//     XML-элемента объекта, единственное число на английском (Catalog,
//     InformationRegister, ...);
//   - internal/parse/query.Table.Name — префикс на русском (или английском)
//     в тексте запроса (Справочник, РегистрСведений, ...).
//
// Сопоставление этих словарей — работа резолвера (join уже извлечённых
// фактов по идентичности объекта), а не второй парсер.

// bslCollectionToMType переводит множественное число каталога выгрузки
// (bsl.ManagerRef.MetaType, bsl.RegisterAccess.MetaType) в MType объекта
// метаданных (parse/meta.MetadataObjectFact.MType). Словарь один на все слои,
// domain.MetaKinds; ok=false, если вид не описан.
func bslCollectionToMType(dir string) (string, bool) {
	k, ok := domain.MetaKindByDumpDir(dir)
	return k.MType, ok
}

// registerMTypes: MType видов с наборами записей и менеджерами записи
// (domain.MetaKind.IsRegister), используется для разрешения Движения.Х:
// MetaType у такого RegisterAccess пуст, поиск объекта идёт по имени среди
// этих видов. Выборка из domain.MetaKinds делается один раз, не на каждый
// доступ к регистру.
var registerMTypes = func() []string {
	var out []string
	for _, k := range domain.MetaKinds() {
		if k.IsRegister {
			out = append(out, k.MType)
		}
	}
	return out
}()

// queryPrefixToMType переводит префикс имени таблицы из текста запроса
// (parse/query.Table.Name до первой точки, регистронезависимо) в MType.
// Виртуальные таблицы регистра пишутся тем же префиксом, что и обычная
// таблица (РегистрНакопления.Х.Остатки) — Table.VirtualKind различает их
// отдельно, префикс здесь общий.
//
// упрощение: не все ~20 видов объектов метаданных 1С перечислены — только
// те, что встречаются в типовых запросах и покрыты фикстурами/реальной
// выгрузкой этого прогона. Список закрыт намеренно, не является границей
// архитектуры: расширяется добавлением строки. В domain.MetaKinds не сведён:
// это другой словарь (единственное число на русском в тексте запроса), и
// новый вид в нём меняет результат разрешения запросов.
var queryPrefixToMType = map[string]string{
	"справочник":                 "Catalog",
	"catalog":                    "Catalog",
	"документ":                   "Document",
	"document":                   "Document",
	"перечисление":               "Enum",
	"enum":                       "Enum",
	"регистрсведений":            "InformationRegister",
	"informationregister":        "InformationRegister",
	"регистрнакопления":          "AccumulationRegister",
	"accumulationregister":       "AccumulationRegister",
	"регистрбухгалтерии":         "AccountingRegister",
	"accountingregister":         "AccountingRegister",
	"регистррасчета":             "CalculationRegister",
	"calculationregister":        "CalculationRegister",
	"планвидовхарактеристик":     "ChartOfCharacteristicTypes",
	"chartofcharacteristictypes": "ChartOfCharacteristicTypes",
	"плансчетов":                 "ChartOfAccounts",
	"chartofaccounts":            "ChartOfAccounts",
	"планвидоврасчета":           "ChartOfCalculationTypes",
	"chartofcalculationtypes":    "ChartOfCalculationTypes",
	"бизнеспроцесс":              "BusinessProcess",
	"businessprocess":            "BusinessProcess",
	"задача":                     "Task",
	"task":                       "Task",
	"планобмена":                 "ExchangePlan",
	"exchangeplan":               "ExchangePlan",
	"константа":                  "Constant",
	"constant":                   "Constant",
}

// refTypeMType переводит XDTO-имя ссылочного типа (как в исходнике
// meta.MetadataMemberFact.Types, например "cfg:CatalogRef.Товары") в MType +
// нормализованное имя объекта. Суффикс "Ref" — часть закрытой платформенной
// конвенции именования ссылочных типов (CatalogRef/DocumentRef/EnumRef/...),
// само имя без суффикса УЖЕ совпадает с MType — отдельная таблица не нужна,
// в отличие от bslCollectionToMType/queryPrefixToMType (там имена вида
// совпадают только у части словаря или пишутся во множественном числе).
// ok=false — тип не ссылочный (примитив вроде "xs:string" или не опознан).
func refTypeMType(rawType string) (mtype, nameNorm string, ok bool) {
	// снять префикс пространства имён "cfg:"/"d5p1:"/...
	if i := strings.IndexByte(rawType, ':'); i >= 0 {
		rawType = rawType[i+1:]
	}
	dot := strings.IndexByte(rawType, '.')
	if dot < 0 {
		return "", "", false
	}
	kind, name := rawType[:dot], rawType[dot+1:]
	base, hasRef := strings.CutSuffix(kind, "Ref")
	if !hasRef || base == "" || name == "" {
		return "", "", false
	}
	return base, domain.NormalizeName(name), true
}
