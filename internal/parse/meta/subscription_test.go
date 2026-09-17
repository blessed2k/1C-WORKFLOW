package meta

import "testing"

// Три фикстуры — дословно EventSubscriptions/*.xml реальной выгрузки УТ,
// по одной на каждую форму источника. Ожидания взяты из документированного
// поведения internal/source/writepath.go (образец семантики, не вызывается
// отсюда): подписка на конкретный тип, подписка на голый вид (TypeSet без
// DefinedType — "весь класс", в УТ это КАЖДЫЙ документ) и подписка на
// ОпределяемыйТип — обе формы TypeSet обязаны быть распознаны, иначе теряется
// 128 из 308 подписок УТ.

const subscriptionTypeFixture = "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
	<EventSubscription uuid="bdeb97e9-5f41-4896-9d7c-8d15963a63ab">
		<Properties>
			<Name>ВариантОтчетаПередУдалением</Name>
			<Synonym>
				<v8:item>
					<v8:lang>ru</v8:lang>
					<v8:content>Вариант отчета перед удалением</v8:content>
				</v8:item>
			</Synonym>
			<Comment/>
			<Source>
				<v8:Type>cfg:CatalogObject.ВариантыОтчетов</v8:Type>
			</Source>
			<Event>BeforeDelete</Event>
			<Handler>CommonModule.МониторингЦелевыхПоказателей.ВариантОтчетаПередУдалением</Handler>
		</Properties>
	</EventSubscription>
</MetaDataObject>`

const subscriptionBareKindFixture = "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
	<EventSubscription uuid="b9a6b053-2b93-4946-9531-040d3131dcc5">
		<Properties>
			<Name>ВключитьИспользованиеПланаОбмена</Name>
			<Comment/>
			<Source>
				<v8:TypeSet>cfg:ExchangePlanObject</v8:TypeSet>
			</Source>
			<Event>BeforeWrite</Event>
			<Handler>CommonModule.ОбменДаннымиСобытия.ВключитьИспользованиеПланаОбмена</Handler>
		</Properties>
	</EventSubscription>
</MetaDataObject>`

const subscriptionDefinedTypeFixture = "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
	<EventSubscription uuid="b75fa87b-bdab-4865-9abe-22d86e0c6d04">
		<Properties>
			<Name>ВлияющийНаСтатусПоступленияКиЗДокументПередЗаписью</Name>
			<Comment/>
			<Source>
				<v8:TypeSet>cfg:DefinedType.ВлияющийНаСтатусПоступленияКиЗДокумент</v8:TypeSet>
			</Source>
			<Event>BeforeWrite</Event>
			<Handler>CommonModule.ИнтеграцияГИСМ.ВлияющийНаСтатусПоступленияКиЗДокументПередЗаписью</Handler>
		</Properties>
	</EventSubscription>
</MetaDataObject>`

func TestParseSubscriptionSourceType(t *testing.T) {
	facts, diags := ParseFile("EventSubscriptions/ВариантОтчетаПередУдалением.xml", []byte(subscriptionTypeFixture))
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %+v", diags)
	}
	s := facts.Subscription
	if s == nil {
		t.Fatal("Subscription не заполнен")
	}
	if len(s.Sources) != 1 {
		t.Fatalf("Sources = %+v, хотели один источник", s.Sources)
	}
	if s.Sources[0].Kind != SourceKindType || s.Sources[0].Name != "CatalogObject.ВариантыОтчетов" {
		t.Errorf("Sources[0] = %+v", s.Sources[0])
	}
	if s.Event != "BeforeDelete" {
		t.Errorf("Event = %q", s.Event)
	}
	if s.HandlerRaw != "CommonModule.МониторингЦелевыхПоказателей.ВариантОтчетаПередУдалением" {
		t.Errorf("HandlerRaw = %q", s.HandlerRaw)
	}
}

// Голый вид: подписка не называет конкретный объект, а весь класс
// (ExchangePlanObject — КАЖДЫЙ план обмена). Чтение только <v8:Type> теряет
// эту подписку целиком (writepath.go, комментарий у xmlSourceType).
func TestParseSubscriptionSourceBareKind(t *testing.T) {
	facts, _ := ParseFile("EventSubscriptions/ВключитьИспользованиеПланаОбмена.xml", []byte(subscriptionBareKindFixture))
	s := facts.Subscription
	if s == nil || len(s.Sources) != 1 {
		t.Fatalf("Subscription = %+v", s)
	}
	if s.Sources[0].Kind != SourceKindBareKind {
		t.Errorf("Kind = %q, хотели bare-kind", s.Sources[0].Kind)
	}
	if s.Sources[0].Name != "ExchangePlanObject" {
		t.Errorf("Name = %q", s.Sources[0].Name)
	}
}

// ОпределяемыйТип: TypeSet называет DefinedType, а не голый вид и не
// конкретный тип — третья различимая форма источника.
func TestParseSubscriptionSourceDefinedType(t *testing.T) {
	facts, _ := ParseFile("EventSubscriptions/ВлияющийНаСтатусПоступленияКиЗДокументПередЗаписью.xml", []byte(subscriptionDefinedTypeFixture))
	s := facts.Subscription
	if s == nil || len(s.Sources) != 1 {
		t.Fatalf("Subscription = %+v", s)
	}
	if s.Sources[0].Kind != SourceKindDefinedType {
		t.Errorf("Kind = %q, хотели defined-type", s.Sources[0].Kind)
	}
	if s.Sources[0].Name != "ВлияющийНаСтатусПоступленияКиЗДокумент" {
		t.Errorf("Name = %q, префикс DefinedType. должен быть снят", s.Sources[0].Name)
	}
}
