package source

import (
	"context"
	"testing"
)

func issueCodes(rep *ExchangeAuditReport) map[string]bool {
	out := map[string]bool{}
	for _, f := range rep.Findings {
		out[f.Code] = true
	}
	return out
}

// TestExchangeAuditRegisteredBySubscription is the case a БСП configuration is
// built on: the plan denies auto-registration, and a subscription does the
// registration instead. That is not a defect and must not be reported as one.
func TestExchangeAuditRegisteredBySubscription(t *testing.T) {
	rep, err := wpSource(t).ExchangeAudit(context.Background(), "Document", "ЗаказКлиента")
	if err != nil {
		t.Fatalf("ExchangeAudit: %v", err)
	}
	if len(rep.InPlans) != 1 || rep.InPlans[0].Plan != "ОбменСБухгалтерией" {
		t.Fatalf("inPlans = %+v", rep.InPlans)
	}
	if rep.InPlans[0].AutoRecord {
		t.Error("AutoRecord=Deny must be reported as false")
	}
	if !rep.InPlans[0].Distributed {
		t.Error("a plan with DistributedInfoBase=true must be marked as РИБ")
	}
	if len(rep.Registrars) == 0 {
		t.Fatalf("the registering subscription was not found: %+v", rep)
	}
	if issueCodes(rep)["NoRegistration"] {
		t.Errorf("registration by subscription must not be reported as a defect: %+v", rep.Findings)
	}
}

// TestExchangeAuditNoRegistration is the actual defect: denied auto-registration
// and nobody registering the object.
func TestExchangeAuditNoRegistration(t *testing.T) {
	rep, err := wpSource(t).ExchangeAudit(context.Background(), "InformationRegister", "ЦеныНоменклатуры")
	if err != nil {
		t.Fatalf("ExchangeAudit: %v", err)
	}
	if !issueCodes(rep)["NoRegistration"] {
		t.Errorf("denied auto-registration without a registrar must be reported: %+v", rep)
	}
}

func TestExchangeAuditNotInAnyPlan(t *testing.T) {
	rep, err := wpSource(t).ExchangeAudit(context.Background(), "Catalog", "Склады")
	if err != nil {
		t.Fatalf("ExchangeAudit: %v", err)
	}
	if len(rep.InPlans) != 0 {
		t.Fatalf("inPlans = %+v, want none", rep.InPlans)
	}
	if !issueCodes(rep)["NotInAnyPlan"] {
		t.Errorf("an object outside every plan must be reported: %+v", rep.Findings)
	}
	if len(rep.NotInPlans) == 0 {
		t.Error("the plans the object is missing from must be listed")
	}
}

func TestExchangeAuditAutoRecordAllowed(t *testing.T) {
	rep, err := wpSource(t).ExchangeAudit(context.Background(), "AccumulationRegister", "Остатки")
	if err != nil {
		t.Fatalf("ExchangeAudit: %v", err)
	}
	if len(rep.InPlans) != 1 || !rep.InPlans[0].AutoRecord {
		t.Fatalf("AutoRecord=Allow must be reported as true: %+v", rep.InPlans)
	}
	if len(rep.Findings) != 0 {
		t.Errorf("auto-registration needs no findings: %+v", rep.Findings)
	}
}

func TestExchangeAuditErrors(t *testing.T) {
	s := wpSource(t)
	if _, err := s.ExchangeAudit(context.Background(), "", ""); err == nil {
		t.Error("empty arguments must be an error")
	}
	if _, err := s.ExchangeAudit(context.Background(), "Document", "../../etc/passwd"); err == nil {
		t.Error("a path must be rejected")
	}
	if _, err := s.ExchangeAudit(context.Background(), "Document", "НетТакого"); err == nil {
		t.Error("unknown object must be an error")
	}
}

// TestExchangeAuditRegistrarNeedsRealCall pins that a handler is a registrar only
// when it reaches ПланыОбмена.ЗарегистрироватьИзменения(. Five handlers in УТ are
// NAMED ЗарегистрироватьИзменения* and register nothing: matching the substring
// counted them as registrars.
func TestExchangeAuditRegistrarNeedsRealCall(t *testing.T) {
	if reRegisterCall.MatchString("Процедура ЗарегистрироватьИзмененияФильтров(Источник) Экспорт") {
		t.Error("a procedure name must not count as a registration call")
	}
	if !reRegisterCall.MatchString("\tПланыОбмена.ЗарегистрироватьИзменения(Узел, Источник);") {
		t.Error("the platform call must count")
	}
}

// TestExchangeAuditPerPlanCoverage pins that the report says what registers each
// plan, which is the actual answer to "почему объект не ушёл".
func TestExchangeAuditPerPlanCoverage(t *testing.T) {
	rep, err := wpSource(t).ExchangeAudit(context.Background(), "AccumulationRegister", "Остатки")
	if err != nil {
		t.Fatalf("ExchangeAudit: %v", err)
	}
	if len(rep.InPlans) != 1 || rep.InPlans[0].RegisteredBy == "" {
		t.Fatalf("a plan with auto-registration must say so: %+v", rep.InPlans)
	}
}
