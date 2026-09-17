package source

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestRealExport runs XMLSource against a real 1C XML export when ONEC_REAL_DUMP
// points at one. It is skipped by default so the suite stays hermetic and
// portable.
func TestRealExport(t *testing.T) {
	root := os.Getenv("ONEC_REAL_DUMP")
	if root == "" {
		t.Skip("set ONEC_REAL_DUMP to a 1C XML export directory to run this")
	}
	ctx := context.Background()
	s := NewXMLSource(root)

	info, err := s.ConfigurationInfo(ctx)
	if err != nil {
		t.Fatalf("ConfigurationInfo: %v", err)
	}
	t.Logf("config: name=%q vendor=%q version=%q ext=%v objectsByType=%v",
		info.Name, info.Vendor, info.Version, info.IsExtension, info.ObjectCounts)
	if info.Name == "" {
		t.Error("empty configuration name")
	}

	tree, err := s.MetadataTree(ctx)
	if err != nil {
		t.Fatalf("MetadataTree: %v", err)
	}
	t.Logf("tree: %d objects across %d types", tree.TotalObjects, len(tree.Groups))
	if tree.TotalObjects == 0 {
		t.Error("no objects in metadata tree")
	}

	// Object to inspect: ONEC_REAL_OBJECT="Type:Name" overrides the default
	// (first Catalog / first group).
	objType, objName := "", ""
	if spec := os.Getenv("ONEC_REAL_OBJECT"); spec != "" {
		if parts := strings.SplitN(spec, ":", 2); len(parts) == 2 {
			objType, objName = parts[0], parts[1]
		}
	} else if g := pickGroup(tree.Groups); g != nil && len(g.Objects) > 0 {
		objType, objName = g.Type, g.Objects[0]
	}
	if objType != "" {
		obj, err := s.ObjectStructure(ctx, objType, objName)
		if err != nil {
			t.Fatalf("ObjectStructure(%s.%s): %v", objType, objName, err)
		}
		t.Logf("object %s.%s: %d attributes, %d tabular sections, %d forms",
			obj.Type, obj.Name, len(obj.Attributes), len(obj.TabularSections), len(obj.Forms))
		for _, a := range obj.Attributes {
			t.Logf("  attr %s : %v", a.Name, a.Type)
		}
		if obj.Name != objName {
			t.Errorf("parsed name %q != requested %q", obj.Name, objName)
		}
	}

	// Form to inspect: ONEC_REAL_FORM="Type:Name:Form" (or "CommonForm:Name:").
	if spec := os.Getenv("ONEC_REAL_FORM"); spec != "" {
		p := strings.SplitN(spec, ":", 3)
		for len(p) < 3 {
			p = append(p, "")
		}
		form, err := s.FormStructure(ctx, p[0], p[1], p[2])
		if err != nil {
			t.Fatalf("FormStructure(%v): %v", spec, err)
		}
		t.Logf("form %s (owner %s): %d attributes, %d items, %d commands, %d handlers",
			form.Name, form.Owner, len(form.Attributes), len(form.Items), len(form.Commands), len(form.Handlers))
		for _, h := range form.Handlers {
			t.Logf("  handler %s.%s -> %s", h.Source, h.Event, h.Handler)
		}
	}

	res, err := s.SearchCode(ctx, SearchParams{Query: "Процедура", IgnoreCase: true, MaxResults: 5})
	if err != nil {
		t.Fatalf("SearchCode: %v", err)
	}
	t.Logf("search 'Процедура': %d matches (truncated=%v)", len(res.Matches), res.Truncated)
	for _, m := range res.Matches {
		t.Logf("  %s:%d  %s", m.File, m.Line, m.Text)
	}
}

// pickGroup prefers a Catalog group (rich attributes/forms) for the object
// structure check, falling back to the first available group.
func pickGroup(groups []MetadataGroup) *MetadataGroup {
	for i := range groups {
		if groups[i].Type == "Catalog" {
			return &groups[i]
		}
	}
	if len(groups) > 0 {
		return &groups[0]
	}
	return nil
}
