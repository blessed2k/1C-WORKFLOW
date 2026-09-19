package resolve

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// stitchEndpoints: два проекта: «erp» с сервисом обмена и «shop» с
// сервисом каталога. Имена вымышленные.
func stitchEndpoints() map[domain.ProjectID][]HTTPEndpointFact {
	return map[domain.ProjectID][]HTTPEndpointFact{
		"erp": {
			{Project: "erp", ID: 1, ServiceID: 10, RootURL: "exchange", Template: "/v1/orders/{Номер}", MethodName: "get", HTTPMethod: "GET", Handler: "ЗаказGet"},
			{Project: "erp", ID: 2, ServiceID: 10, RootURL: "exchange", Template: "/v1/orders/{Номер}", MethodName: "post", HTTPMethod: "POST", Handler: "ЗаказPost"},
			{Project: "erp", ID: 3, ServiceID: 10, RootURL: "exchange", Template: "/version", MethodName: "get", HTTPMethod: "GET", Handler: "ВерсияGet"},
			{Project: "erp", ID: 6, ServiceID: 10, RootURL: "exchange", Template: "/v1/orders/{Номер}/confirm", MethodName: "post", HTTPMethod: "POST", Handler: "ПодтверждениеPost"},
			{Project: "erp", ID: 4, ServiceID: 11, RootURL: "files", Template: "/*", MethodName: "any", HTTPMethod: "ANY", Handler: "ФайлыAny"},
		},
		"shop": {
			{Project: "shop", ID: 5, ServiceID: 20, RootURL: "catalog", Template: "/items", MethodName: "get", HTTPMethod: "GET", Handler: "ТоварыGet"},
		},
	}
}

func stitchHosts(host string) (domain.ProjectID, bool) {
	switch host {
	case "erp.example.local":
		return "erp", true
	case "shop.example.local":
		return "shop", true
	case "archive.example.local":
		return "archive", true
	}
	return "", false
}

// TestStitchHTTPCall: правила сшивки ADR-039 по одному на строку.
func TestStitchHTTPCall(t *testing.T) {
	cases := []struct {
		name     string
		call     HTTPCallFact
		kind     HTTPStitchKind
		reason   string
		handlers []string
		conf     float64
	}{
		{"мапленный хост и полный путь дают обработчик",
			HTTPCallFact{Verb: "POST", Host: "ERP.example.local:443", HostStatic: true, Path: "/erp/hs/exchange/v1/orders/42", PathKind: "static"},
			HTTPStitched, ReasonHostMapped, []string{"ЗаказPost"}, StitchMappedStatic},
		{"хост со схемой и строка запроса",
			HTTPCallFact{Verb: "GET", Host: "https://erp.example.local", HostStatic: true, Path: "/erp/hs/exchange/version?full=1", PathKind: "static"},
			HTTPStitched, ReasonHostMapped, []string{"ВерсияGet"}, StitchMappedStatic},
		{"немапленный хост внешний даже при совпадающем пути",
			HTTPCallFact{Verb: "GET", Host: "api.partner.example", HostStatic: true, Path: "/erp/hs/exchange/version", PathKind: "static"},
			HTTPExternal, ReasonHostUnmapped, nil, 0},
		{"мапленный хост, но сервиса в своём проекте нет",
			HTTPCallFact{Verb: "GET", Host: "shop.example.local", HostStatic: true, Path: "/erp/hs/exchange/version", PathKind: "static"},
			HTTPExternal, ReasonNoEndpoint, nil, 0},
		{"мапленный хост на незагруженный проект",
			HTTPCallFact{Verb: "GET", Host: "archive.example.local", HostStatic: true, Path: "/a/hs/x/y", PathKind: "static"},
			HTTPExternal, ReasonProjectNotLoaded, nil, 0},
		{"динамический путь ребра не даёт",
			HTTPCallFact{Verb: "GET", Host: "erp.example.local", HostStatic: true, PathKind: "dynamic"},
			HTTPDynamic, ReasonDynamicPath, nil, 0},
		{"вычисляемый хост: сшивка по пути с меньшей достоверностью",
			HTTPCallFact{Verb: "GET", Path: "/shop/hs/catalog/items", PathKind: "static"},
			HTTPStitched, ReasonPathOnly, []string{"ТоварыGet"}, StitchPathOnlyStatic},
		{"начало пути с параметром-хвостом",
			HTTPCallFact{Verb: "GET", Host: "erp.example.local", HostStatic: true, Path: "/erp/hs/exchange/v1/orders/", PathKind: "prefix"},
			HTTPStitched, ReasonHostMapped, []string{"ЗаказGet"}, StitchMappedPrefix},
		{"начало и статический конец выбирают один шаблон",
			HTTPCallFact{Verb: "POST", Host: "erp.example.local", HostStatic: true, Path: "/erp/hs/exchange/v1/orders/", PathKind: "prefix", PathSuffix: "/confirm"},
			HTTPStitched, ReasonHostMapped, []string{"ПодтверждениеPost"}, StitchMappedPrefix},
		{"статический конец, которого нет ни у одного шаблона",
			HTTPCallFact{Verb: "GET", Host: "erp.example.local", HostStatic: true, Path: "/erp/hs/exchange/v1/", PathKind: "prefix", PathSuffix: "/missing/extra"},
			HTTPExternal, ReasonNoEndpoint, nil, 0},
		{"начало пути, оборванное внутри корня",
			HTTPCallFact{Verb: "GET", Host: "erp.example.local", HostStatic: true, Path: "/erp/hs/exch", PathKind: "prefix"},
			HTTPExternal, ReasonPrefixBeforeRoot, nil, 0},
		{"шаблон со звёздочкой забирает остаток",
			HTTPCallFact{Verb: "PUT", Host: "erp.example.local", HostStatic: true, Path: "/erp/hs/files/a/b/c.txt", PathKind: "static"},
			HTTPStitched, ReasonHostMapped, []string{"ФайлыAny"}, StitchMappedStatic},
		{"метод вызова шаблон не обрабатывает",
			HTTPCallFact{Verb: "DELETE", Host: "erp.example.local", HostStatic: true, Path: "/erp/hs/exchange/version", PathKind: "static"},
			HTTPStitched, ReasonVerbNotAllowed, []string{"ВерсияGet"}, StitchMappedStatic},
		{"лишний сегмент не совпадает с шаблоном",
			HTTPCallFact{Verb: "GET", Host: "erp.example.local", HostStatic: true, Path: "/erp/hs/exchange/version/extra", PathKind: "static"},
			HTTPExternal, ReasonNoEndpoint, nil, 0},
		{"начало пути без /hs/ тоже не сервис 1С",
			HTTPCallFact{Verb: "GET", Path: "/epd/v1/GetQR/?uid=", PathKind: "prefix"},
			HTTPExternal, ReasonNoEndpoint, nil, 0},
		{"путь без /hs/ не сервис 1С",
			HTTPCallFact{Verb: "GET", Path: "/api/v2/items", PathKind: "static"},
			HTTPExternal, ReasonNoEndpoint, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StitchHTTPCall(tc.call, stitchEndpoints(), stitchHosts)
			if got.Kind != tc.kind || got.Reason != tc.reason {
				t.Fatalf("исход %s/%s, ожидался %s/%s (%+v)", got.Kind, got.Reason, tc.kind, tc.reason, got)
			}
			var handlers []string
			for _, ep := range got.Endpoints {
				handlers = append(handlers, ep.Handler)
			}
			if len(handlers) != len(tc.handlers) {
				t.Fatalf("обработчики %v, ожидались %v", handlers, tc.handlers)
			}
			for i := range handlers {
				if handlers[i] != tc.handlers[i] {
					t.Fatalf("обработчики %v, ожидались %v", handlers, tc.handlers)
				}
			}
			if got.Confidence != tc.conf {
				t.Errorf("confidence %v, ожидался %v", got.Confidence, tc.conf)
			}
			if got.Confidence >= 1 {
				t.Errorf("сшивка по пути эвристическая, confidence 1 запрещён")
			}
		})
	}
}

func TestNormalizeHTTPHost(t *testing.T) {
	for raw, want := range map[string]string{
		"erp.example.local":                           "erp.example.local",
		"https://user:pw@ERP.Example.Local:8443/base": "erp.example.local",
		"erp.example.local.":                          "erp.example.local",
		"[::1]:80":                                    "[::1]",
	} {
		if got := domain.NormalizeHTTPHost(raw); got != want {
			t.Errorf("NormalizeHTTPHost(%q) = %q, ожидалось %q", raw, got, want)
		}
	}
}

// TestAttributeSymbolFact: HTTP-вызов в общем модуле приписывается тем же
// документам, что и запись в регистр из того же места (D6): сквозь общий
// модуль вверх до владельцев, достоверность: минимум по цепочке.
func TestAttributeSymbolFact(t *testing.T) {
	g := newGraph().
		symbolInCommon(1, 900, 9).  // ОбменСЕРП.ОтправитьЗаказ, в нём вызов
		symbolInObject(2, 100, 10). // Документ.ЗаказКлиента, модуль объекта
		symbolInObject(3, 200, 20). // Документ.Возврат, модуль объекта
		symbolInObject(4, 100, 11). // второй путь к тому же документу
		calledFrom(1, 2, 0.9).
		calledFrom(1, 3, 0.7).
		calledFrom(1, 4, 0.95)
	owners, truncated := AttributeSymbolFact(SymbolFact{SymbolID: 1, FileID: 9, Confidence: 0.85}, g, ObjectEdgeTunables{})
	if truncated {
		t.Fatal("обход не усекался")
	}
	if len(owners) != 2 {
		t.Fatalf("владельцев %d, ожидалось 2: %+v", len(owners), owners)
	}
	if owners[0].ObjectID != 100 || owners[0].Confidence != 0.85 {
		t.Errorf("первый владелец %+v: ожидался 100 с максимумом по цепочкам 0.85", owners[0])
	}
	if owners[1].ObjectID != 200 || owners[1].Confidence != 0.7 {
		t.Errorf("второй владелец %+v: ожидался 200 с минимумом по цепочке 0.7", owners[1])
	}
	if len(owners[0].Chain) != 2 || owners[0].Chain[len(owners[0].Chain)-1].SymbolID != 1 {
		t.Errorf("цепочка от владельца к месту вызова: %+v", owners[0].Chain)
	}

	// Без вызывающих цепочка до объекта не доходит: пустой ответ, не ошибка.
	lonely := newGraph().symbolInCommon(1, 900, 9)
	if owners, _ := AttributeSymbolFact(SymbolFact{SymbolID: 1, FileID: 9, Confidence: 0.85}, lonely, ObjectEdgeTunables{}); len(owners) != 0 {
		t.Errorf("владельцев быть не должно: %+v", owners)
	}
}
