package graphweb_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/graphweb"
)

// TestHandlerPicksUpEpochSwapWithoutRestart: граф «переживает
// пересборку индекса под собой... следующий запрос отвечает из новой эпохи
// без перезапуска». Это транспортный швейный тест на design-decision из
// doc.go (internal/graphweb): ProjectHandle.Open строит СВЕЖИЙ app.Projects
// на каждый HTTP-запрос именно потому, что app.Projects кэширует открытый
// *store.Store навсегда — если бы Handler держал один app.Projects все
// время жизни процесса, второй GET ниже вернул бы всё ещё "O1-v1", а не
// "O1-v2", несмотря на то, что store.Rebuild уже опубликовал новую эпоху.
//
// rebuildTwoNodesWithEdgeNamed имитирует «другой процесс (обычный mcp1c,
// вызванный пользователем через reindex mode=full) пересобрал индекс, пока
// graph-сервер уже отвечал на запросы».
func TestHandlerPicksUpEpochSwapWithoutRestart(t *testing.T) {
	tp := newTestProject(t, "graphweb-epoch")
	o1, _, _, _ := seedTwoNodesWithEdgeNamed(t, tp, "-v1")
	h := graphweb.NewHandler([]graphweb.ProjectHandle{tp.handle})

	rr1, env1 := doGET(t, h, fmt.Sprintf("/api/node/%d", o1))
	if rr1.Code != http.StatusOK {
		t.Fatalf("до пересборки: status = %d, body = %s", rr1.Code, rr1.Body.String())
	}
	name1 := nodeDisplayName(t, env1)
	if name1 != "O1-v1" {
		t.Fatalf("до пересборки: nameDisplay = %q, want O1-v1", name1)
	}

	o1после, _, _, _ := rebuildTwoNodesWithEdgeNamed(t, tp, "-v2")
	if o1после != o1 {
		// Гарантия, на которой держится остальная часть теста (см.
		// doc-комментарий twoNodesWithEdgeFill): тот же порядок вставки в
		// свежую пустую эпоху даёт тот же автоинкремент id.
		t.Fatalf("id узла после пересборки = %d, want %d (тест полагается на совпадение id)", o1после, o1)
	}

	rr2, env2 := doGET(t, h, fmt.Sprintf("/api/node/%d", o1))
	if rr2.Code != http.StatusOK {
		t.Fatalf("после пересборки: status = %d, body = %s", rr2.Code, rr2.Body.String())
	}
	name2 := nodeDisplayName(t, env2)
	if name2 != "O1-v2" {
		t.Fatalf("после пересборки: nameDisplay = %q, want O1-v2 (эпоха не переоткрылась — сервер отвечает старыми данными)", name2)
	}
}

func nodeDisplayName(t *testing.T, env envelope) string {
	t.Helper()
	if len(env.Items) != 1 {
		t.Fatalf("ожидался ровно один узел, получили %d", len(env.Items))
	}
	var node struct {
		NameDisplay string `json:"nameDisplay"`
	}
	if err := json.Unmarshal(env.Items[0], &node); err != nil {
		t.Fatalf("decode node: %v", err)
	}
	return node.NameDisplay
}
