package graphweb

import (
	"net/http"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

// handleCrosslinks: GET /api/crosslinks?projects=a,b (веха В2, §8.1,
// ADR-039): HTTP-связи между открытыми проектами. Без projects: все
// открытые. Каждый проект читается своим свежим сервисом (doc.go), маппинг
// хостов сливается из http-hosts.json всех их workspace в порядке --project.
func (h *Handler) handleCrosslinks(w http.ResponseWriter, r *http.Request) {
	ids := splitCSV(r.URL.Query().Get("projects"))
	var handles []*ProjectHandle
	if len(ids) == 0 {
		for i := range h.projects {
			handles = append(handles, &h.projects[i])
		}
	} else {
		for _, id := range ids {
			ph := h.projectByID(id)
			if ph == nil {
				respondErr(w, app.NewError(codeUnknownProject, "проект "+id+" не открыт",
					"доступные проекты: "+h.projectIDList()))
				return
			}
			handles = append(handles, ph)
		}
	}
	if len(handles) == 0 {
		respondErr(w, app.NewError(app.CodeNoActiveProject, "ни один проект не открыт",
			"запустите mcp1c graph с хотя бы одним --project"))
		return
	}

	roots := make([]string, 0, len(handles))
	facts := make([]app.ProjectHTTPFacts, 0, len(handles))
	snaps := make([]app.Snapshot, 0, len(handles))
	seenProject := map[string]bool{}
	for _, ph := range handles {
		if seenProject[string(ph.ID)] {
			continue
		}
		seenProject[string(ph.ID)] = true
		roots = append(roots, ph.Root)
		gs, closeFn, aerr := h.openGraphService(r, ph)
		if aerr != nil {
			respondErr(w, aerr)
			return
		}
		f, snap, err := gs.HTTPFacts(r.Context())
		closeFn()
		if err != nil {
			respondErr(w, err)
			return
		}
		facts = append(facts, f)
		snaps = append(snaps, snap)
	}
	writeJSON(w, http.StatusOK, app.AssembleCrossLinks(roots, facts, snaps))
}

func (h *Handler) projectByID(id string) *ProjectHandle {
	for i := range h.projects {
		if string(h.projects[i].ID) == id {
			return &h.projects[i]
		}
	}
	return nil
}
