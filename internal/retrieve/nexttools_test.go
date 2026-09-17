package retrieve

import "testing"

// TestNextToolsNameNoRemovedTools: подсказка следующего шага не ведёт на
// удалённый инструмент. posting_review влит в get_movements (review=true) в C3.
func TestNextToolsNameNoRemovedTools(t *testing.T) {
	removed := map[string]bool{"posting_review": true}
	intents := []string{IntentSignatureChange, IntentRegister, IntentForm, IntentAddAttribute,
		IntentQuery, IntentPosting, IntentRights, ""}
	for _, intent := range intents {
		for _, tool := range nextToolsFor(intent, []string{"x"}) {
			if removed[tool] {
				t.Errorf("intent %q: подсказка ведёт на удалённый %s", intent, tool)
			}
		}
	}
	got := nextToolsFor(IntentPosting, nil)
	if len(got) == 0 || got[0] != "get_movements" {
		t.Errorf("posting: %v, want get_movements первым", got)
	}
}
