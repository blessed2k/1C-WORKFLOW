package index

import (
	"fmt"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// httpServiceXML — HTTP-сервис фикстуры; %s подставляет второй шаблон,
// чтобы правка XML меняла состав методов.
const httpServiceXML = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.20">
	<HTTPService uuid="c09df096-f9cc-4b2f-a44e-69147339dc8c">
		<Properties>
			<Name>ОбменЗаказами</Name>
			<RootURL>exchange</RootURL>
		</Properties>
		<ChildObjects>
			<URLTemplate uuid="aa4e4f39-3080-4365-89aa-00b030e26eec">
				<Properties><Name>Версия</Name><Template>/version</Template></Properties>
				<ChildObjects>
					<Method uuid="84c1ba1c-e5d6-46ec-98a3-589499162e67">
						<Properties><Name>get</Name><HTTPMethod>GET</HTTPMethod><Handler>ВерсияGet</Handler></Properties>
					</Method>
				</ChildObjects>
			</URLTemplate>%s
		</ChildObjects>
	</HTTPService>
</MetaDataObject>`

const httpSecondTemplate = `
			<URLTemplate uuid="ba4e4f39-3080-4365-89aa-00b030e26eec">
				<Properties><Name>Заказ</Name><Template>/v1/orders/{Номер}</Template></Properties>
				<ChildObjects>
					<Method uuid="94c1ba1c-e5d6-46ec-98a3-589499162e67">
						<Properties><Name>post</Name><HTTPMethod>POST</HTTPMethod><Handler>ЗаказPost</Handler></Properties>
					</Method>
				</ChildObjects>
			</URLTemplate>`

const httpCallerModule = `
Процедура ОтправитьВерсию() Экспорт
	Соединение = Новый HTTPСоединение("erp.example.local");
	Запрос = Новый HTTPЗапрос("/erp/hs/exchange/version");
	Ответ = Соединение.Получить(Запрос);
КонецПроцедуры

Функция Прочее()
	Возврат %q;
КонецФункции
`

var httpFactEdits = incrementScenario{
	seed: map[string]string{
		workspace.DumpDeclarationPath("HTTPService", "ОбменЗаказами"):                    fmt.Sprintf(httpServiceXML, ""),
		workspace.DumpModulePath("HTTPService", "ОбменЗаказами", workspace.ModuleCommon): "Функция ВерсияGet(Запрос)\nКонецФункции\n",
		workspace.DumpModulePath("CommonModule", "ОбменСЕРП", workspace.ModuleCommon):    fmt.Sprintf(httpCallerModule, "исходный"),
	},
	edit: func(write func(rel, content string), remove func(rel string)) {
		write(workspace.DumpDeclarationPath("HTTPService", "ОбменЗаказами"), fmt.Sprintf(httpServiceXML, httpSecondTemplate))
		write(workspace.DumpModulePath("CommonModule", "ОбменСЕРП", workspace.ModuleCommon), fmt.Sprintf(httpCallerModule, "правка"))
	},
	mustHave: []string{"handler=ЗаказPost", "path=/erp/hs/exchange/version"},
}

// TestHTTPFactsIncrementEqualsCleanRebuild: факты HTTP (веха В2) после
// правки XML сервиса и модуля вызова совпадают с чистой пересборкой.
func TestHTTPFactsIncrementEqualsCleanRebuild(t *testing.T) {
	checkIncrementRows(t, 30, httpFactEdits)
}
