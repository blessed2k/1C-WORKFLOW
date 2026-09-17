package source

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// Depth and answer-size limits. A configuration graph is dense: past four or
// five steps almost any two objects are "linked", so a deeper answer is noise
// rather than information.
const (
	defaultPathDepth = 4
	maxPathDepth     = 6
	defaultPathCount = 5
	maxPathCount     = 20
)

// depEdge is one link of the dependency graph: the object at the other end and
// the human-readable reason the two are linked.
type depEdge struct {
	to  string
	via string
}

// depPred is one predecessor of a node on a shortest path.
type depPred struct {
	node string
	via  string
}

// DependencyPaths reports the shortest chains of links between two metadata
// objects: which attribute, dimension, resource, tabular-section attribute or
// document movement connects them. It answers "how is A related to B", where
// find_metadata_usages answers only the one-step "where is A used".
//
// Links are followed in both directions (a document referencing a catalog is
// the same link as the catalog being referenced by it), so a path may go up and
// down the reference graph, which is how real chains look:
// catalog -> document attribute -> register movements.
func (s *XMLSource) DependencyPaths(_ context.Context, fromType, fromName, toType, toName string, maxDepth, maxPaths int) (*DependencyReport, error) {
	if fromType == "" || fromName == "" || toType == "" || toName == "" {
		return nil, fmt.Errorf("both objects are required: type and name of each")
	}
	if maxDepth <= 0 {
		maxDepth = defaultPathDepth
	}
	if maxDepth > maxPathDepth {
		maxDepth = maxPathDepth
	}
	if maxPaths <= 0 {
		maxPaths = defaultPathCount
	}
	if maxPaths > maxPathCount {
		maxPaths = maxPathCount
	}

	from := metadataLabel(fromType) + "." + fromName
	to := metadataLabel(toType) + "." + toName
	if from == to {
		return nil, fmt.Errorf("both objects are the same: %s", from)
	}

	graph, err := s.buildDependencyGraph()
	if err != nil {
		return nil, err
	}

	rep := &DependencyReport{From: from, To: to, MaxDepth: maxDepth}
	rep.Paths = shortestDependencyPaths(graph, from, to, maxDepth, maxPaths)
	rep.Found = len(rep.Paths)
	switch {
	case rep.Found == 0:
		rep.Note = fmt.Sprintf("Связь не найдена в пределах %d шагов. Учитываются типы полей (реквизиты, измерения, ресурсы, реквизиты табличных частей) и движения документов по регистрам.", maxDepth)
	case rep.Found == maxPaths:
		rep.Note = fmt.Sprintf("Показаны первые %d кратчайших путей, могут быть ещё (лимит maxPaths).", maxPaths)
	}
	return rep, nil
}

// buildDependencyGraph walks the whole export once and returns the undirected
// adjacency list of metadata objects. It is O(number of objects), the same cost
// as MetadataUsages, and is rebuilt per call because XMLSource holds no state.
func (s *XMLSource) buildDependencyGraph() (map[string][]depEdge, error) {
	cfg, err := s.readConfiguration()
	if err != nil {
		return nil, err
	}

	graph := make(map[string][]depEdge)
	link := func(a, b, via string) {
		graph[a] = append(graph[a], depEdge{to: b, via: via})
		graph[b] = append(graph[b], depEdge{to: a, via: via})
	}

	for _, obj := range cfg.ChildObjects.Items {
		ownerType, ownerName := obj.XMLName.Local, obj.Name
		path := filepath.Join(s.root, folderForType(ownerType), ownerName+".xml")
		var root xmlObjectRoot
		if err := readXML(path, &root); err != nil {
			continue // not every child type has an object file with fields
		}
		owner := metadataLabel(ownerType) + "." + ownerName

		for _, item := range root.Object.Properties.RegisterRecords.Items {
			target := labelFromFullName(item)
			if target == "" {
				continue
			}
			link(owner, target, fmt.Sprintf("%s: движения по регистру %s", owner, target))
		}

		for _, ch := range root.Object.ChildObjects.Items {
			switch ch.XMLName.Local {
			case "Attribute", "Dimension", "Resource":
				linkFieldTypes(link, owner, ch.name(), roleName(ch.XMLName.Local), ch.typeList())
			case "TabularSection":
				if ch.ChildObjects == nil {
					continue
				}
				for _, sub := range ch.ChildObjects.Items {
					if sub.XMLName.Local != "Attribute" {
						continue
					}
					linkFieldTypes(link, owner, ch.name()+"."+sub.name(), "Реквизит ТЧ", sub.typeList())
				}
			}
		}
	}
	return graph, nil
}

// linkFieldTypes adds one edge per reference type of a field. A field with a
// composite type links its owner to every referenced object.
func linkFieldTypes(link func(a, b, via string), owner, field, kind string, raw []string) {
	for _, t := range raw {
		ru := russifyType(t)
		target, ok := refTarget(ru)
		if !ok || target == owner {
			continue
		}
		link(owner, target, fmt.Sprintf("%s.%s (%s) типа %s", owner, field, kind, ru))
	}
}

// refTarget turns a russified reference type ("СправочникСсылка.Товары") into a
// graph node label ("Справочник.Товары"). Non-reference types return false.
func refTarget(ru string) (string, bool) {
	head, name, ok := strings.Cut(ru, ".")
	if !ok || name == "" {
		return "", false
	}
	kind, found := strings.CutSuffix(head, "Ссылка")
	if !found || kind == "" {
		return "", false
	}
	return kind + "." + name, true
}

// labelFromFullName turns an English full name from the export
// ("AccumulationRegister.ТоварыНаСкладах") into a node label.
func labelFromFullName(full string) string {
	kind, name, ok := strings.Cut(strings.TrimSpace(full), ".")
	if !ok || name == "" {
		return ""
	}
	return metadataLabel(kind) + "." + name
}

// shortestDependencyPaths returns up to maxPaths paths of the shortest length
// between from and to, or nil when they are not connected within maxDepth.
// Only shortest paths are reported: longer detours through the same nodes carry
// no extra information about how the two objects are related.
func shortestDependencyPaths(graph map[string][]depEdge, from, to string, maxDepth, maxPaths int) []DependencyPath {
	if len(graph[from]) == 0 {
		return nil
	}

	dist := map[string]int{from: 0}
	preds := make(map[string][]depPred)
	frontier := []string{from}
	for level := 0; level < maxDepth && len(frontier) > 0; level++ {
		var next []string
		for _, node := range frontier {
			for _, e := range graph[node] {
				d, seen := dist[e.to]
				switch {
				case !seen:
					dist[e.to] = level + 1
					preds[e.to] = append(preds[e.to], depPred{node: node, via: e.via})
					next = append(next, e.to)
				case d == level+1:
					// Another equally short way into the same node: keep it, it
					// is a distinct path.
					preds[e.to] = append(preds[e.to], depPred{node: node, via: e.via})
				}
			}
		}
		if _, reached := dist[to]; reached {
			break // the whole level is processed, so every shortest path is recorded
		}
		frontier = next
	}
	if _, reached := dist[to]; !reached {
		return nil
	}

	var paths []DependencyPath
	var walk func(node string, tail []DependencyStep)
	walk = func(node string, tail []DependencyStep) {
		if len(paths) >= maxPaths {
			return
		}
		if node == from {
			steps := make([]DependencyStep, len(tail))
			for i, st := range tail {
				steps[len(tail)-1-i] = st // tail is collected target-first
			}
			paths = append(paths, DependencyPath{Length: len(steps), Steps: steps})
			return
		}
		for _, p := range preds[node] {
			grown := make([]DependencyStep, len(tail), len(tail)+1)
			copy(grown, tail)
			grown = append(grown, DependencyStep{From: p.node, To: node, Via: p.via})
			walk(p.node, grown)
		}
	}
	walk(to, nil)
	return paths
}
