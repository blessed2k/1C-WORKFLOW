package meta

import "testing"

const httpServiceFixture = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
	<HTTPService uuid="c09df096-f9cc-4b2f-a44e-69147339dc8c">
		<Properties>
			<Name>ОбменЗаказами</Name>
			<Synonym><v8:item><v8:lang>ru</v8:lang><v8:content>Обмен заказами</v8:content></v8:item></Synonym>
			<Comment/>
			<RootURL>exchange</RootURL>
			<ReuseSessions>AutoUse</ReuseSessions>
		</Properties>
		<ChildObjects>
			<URLTemplate uuid="aa4e4f39-3080-4365-89aa-00b030e26eec">
				<Properties>
					<Name>Заказ</Name>
					<Template>/v1/orders/{Номер}</Template>
				</Properties>
				<ChildObjects>
					<Method uuid="84c1ba1c-e5d6-46ec-98a3-589499162e67">
						<Properties>
							<Name>get</Name>
							<HTTPMethod>GET</HTTPMethod>
							<Handler>ЗаказGet</Handler>
						</Properties>
					</Method>
					<Method uuid="94c1ba1c-e5d6-46ec-98a3-589499162e67">
						<Properties>
							<Name>post</Name>
							<HTTPMethod>POST</HTTPMethod>
							<Handler>ЗаказPost</Handler>
						</Properties>
					</Method>
				</ChildObjects>
			</URLTemplate>
			<URLTemplate uuid="ba4e4f39-3080-4365-89aa-00b030e26eec">
				<Properties>
					<Name>Версия</Name>
					<Template>/version</Template>
				</Properties>
				<ChildObjects/>
			</URLTemplate>
		</ChildObjects>
	</HTTPService>
</MetaDataObject>`

// TestHTTPServiceFacts: корневой URL, шаблоны и методы с обработчиками
// читаются из XML сервиса (принимающая сторона сшивки, веха В2).
func TestHTTPServiceFacts(t *testing.T) {
	facts, diags := ParseFile("HTTPServices/ОбменЗаказами.xml", []byte(httpServiceFixture))
	if len(diags) != 0 {
		t.Fatalf("диагностики: %+v", diags)
	}
	if facts.Object == nil || facts.Object.MType != "HTTPService" {
		t.Fatalf("объект: %+v", facts.Object)
	}
	s := facts.HTTPService
	if s == nil {
		t.Fatal("HTTPService не разобран")
	}
	if s.RootURL != "exchange" || facts.Object.Props["RootURL"] != "exchange" {
		t.Errorf("RootURL %q, в свойствах %q", s.RootURL, facts.Object.Props["RootURL"])
	}
	if len(s.Templates) != 2 {
		t.Fatalf("шаблонов %d: %+v", len(s.Templates), s.Templates)
	}
	tpl := s.Templates[0]
	if tpl.NameDisplay != "Заказ" || tpl.Template != "/v1/orders/{Номер}" || len(tpl.Methods) != 2 {
		t.Fatalf("шаблон: %+v", tpl)
	}
	if m := tpl.Methods[1]; m.HTTPMethod != "POST" || m.Handler != "ЗаказPost" || m.NameDisplay != "post" {
		t.Errorf("метод: %+v", m)
	}
	if s.Templates[1].Template != "/version" || len(s.Templates[1].Methods) != 0 {
		t.Errorf("шаблон без методов: %+v", s.Templates[1])
	}
	// Шаблоны по-прежнему видны членами объекта (прежний выход не пропал).
	var members int
	for _, m := range facts.Members {
		if m.Kind == "URLTemplate" {
			members++
		}
	}
	if members != 2 {
		t.Errorf("членов URLTemplate %d, ожидалось 2", members)
	}
}
