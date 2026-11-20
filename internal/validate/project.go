package validate

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Component is a root of the project on disk: the configuration dump or the
// dump of one of its extensions.
type Component struct {
	ID        string
	Root      string // absolute
	Extension bool
}

// DiskProject resolves common modules, objects and forms straight from the
// XML dumps of the configuration and its extensions. It reads the disk and not
// the SQLite index on purpose: the drafts it checks are newer than any index,
// and an extension changed after the last reindex would otherwise be judged by
// its old text.
type DiskProject struct {
	mu    sync.Mutex
	comps []Component
	// modules caches resolved common modules for the life of the project
	// value (one MCP call, one CLI run).
	modules map[string]*CommonModule
	globals map[string]bool // export names of global common modules, lower-cased
	attrs   map[string]bool // common attributes, lower-cased
}

// NewDiskProject builds a project over the given components.
func NewDiskProject(comps []Component) *DiskProject {
	p := &DiskProject{modules: map[string]*CommonModule{}}
	for _, c := range comps {
		p.add(c)
	}
	return p
}

func (p *DiskProject) add(c Component) {
	if abs, err := filepath.Abs(c.Root); err == nil {
		c.Root = abs
	}
	for _, have := range p.comps {
		if samePath(have.Root, c.Root) {
			return
		}
	}
	p.comps = append(p.comps, c)
	p.modules = map[string]*CommonModule{}
	p.globals = nil
	p.attrs = nil
}

// Components lists the components the project reads.
func (p *DiskProject) Components() []Component {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Component(nil), p.comps...)
}

// Complete reports that the configuration itself is one of the components.
func (p *DiskProject) Complete() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.comps {
		if !c.Extension {
			return true
		}
	}
	return false
}

// ComponentFor finds the component holding path. When none of the known ones
// does, it looks up the tree for a Configuration.xml and adds that dump as a
// component of its own: an extension the manifest does not list is still
// checked against its own modules.
func (p *DiskProject) ComponentFor(path string) (Component, string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Component{}, "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, rel, ok := p.componentOf(abs); ok {
		return c, rel, true
	}
	for dir := filepath.Dir(abs); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "Configuration.xml")); err == nil {
			c := Component{ID: filepath.Base(dir), Root: dir, Extension: IsExtensionDump(dir)}
			p.add(c)
			rel, _ := filepath.Rel(dir, abs)
			return c, filepath.ToSlash(rel), true
		}
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	return Component{}, "", false
}

func (p *DiskProject) componentOf(abs string) (Component, string, bool) {
	best := -1
	for i, c := range p.comps {
		rel, err := filepath.Rel(c.Root, abs)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			continue
		}
		if best < 0 || len(c.Root) > len(p.comps[best].Root) {
			best = i
		}
	}
	if best < 0 {
		return Component{}, "", false
	}
	rel, _ := filepath.Rel(p.comps[best].Root, abs)
	return p.comps[best], filepath.ToSlash(rel), true
}

// IsExtensionDump reports that a dump directory holds an extension: its
// Configuration.xml declares an extension purpose.
func IsExtensionDump(dir string) bool {
	head, err := readHead(filepath.Join(dir, "Configuration.xml"), 64<<10)
	if err != nil {
		return false
	}
	return bytes.Contains(head, []byte("<ConfigurationExtensionPurpose>"))
}

// CommonModule merges a common module over the components.
func (p *DiskProject) CommonModule(name string) (CommonModule, bool) {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return CommonModule{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if cm, ok := p.modules[lower]; ok {
		if cm == nil {
			return CommonModule{}, false
		}
		return *cm, true
	}
	var cm *CommonModule
	for _, c := range p.comps {
		entry, ok := commonModuleIndex(c.Root)[lower]
		if !ok {
			continue
		}
		info := readMetaInfo(entry.xml)
		if cm == nil {
			cm = &CommonModule{Name: entry.name, Methods: map[string]MethodInfo{}}
		}
		cm.Global = cm.Global || info.global
		cm.Availability.Client = cm.Availability.Client || info.avail.Client
		cm.Availability.Server = cm.Availability.Server || info.avail.Server
		bsl := filepath.Join(c.Root, "CommonModules", entry.name, "Ext", "Module.bsl")
		decls, read := readDecls(bsl)
		for k, d := range decls {
			if have, ok := cm.Methods[k]; ok && have.Export {
				continue
			}
			cm.Methods[k] = d
		}
		// A borrowed module carries only what the extension adds; the module
		// is known in full only where it is defined.
		if !info.adopted && (read || !fileExists(bsl)) {
			cm.Complete = true
		}
	}
	p.modules[lower] = cm
	if cm == nil {
		return CommonModule{}, false
	}
	return *cm, true
}

// GlobalExport reports an export method of a global common module.
func (p *DiskProject) GlobalExport(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.globals == nil {
		p.globals = map[string]bool{}
		for _, c := range p.comps {
			for _, e := range commonModuleIndex(c.Root) {
				if !readMetaInfo(e.xml).global {
					continue
				}
				decls, _ := readDecls(filepath.Join(c.Root, "CommonModules", e.name, "Ext", "Module.bsl"))
				for k, d := range decls {
					if d.Export {
						p.globals[k] = true
					}
				}
			}
		}
	}
	return p.globals[strings.ToLower(strings.TrimSpace(name))]
}

// commonAttributes lists the common attributes of all components: an object
// module reaches them by bare name.
func (p *DiskProject) commonAttributes() map[string]bool {
	if p.attrs != nil {
		return p.attrs
	}
	p.attrs = map[string]bool{}
	for _, c := range p.comps {
		for lower := range dirXMLNames(filepath.Join(c.Root, "CommonAttributes")) {
			p.attrs[lower] = true
		}
	}
	return p.attrs
}

// cmEntry is a common module found in a component.
type cmEntry struct {
	name string // as the file is named
	xml  string
}

// commonModuleIndex lists CommonModules/*.xml of a dump by lower-cased name.
func commonModuleIndex(root string) map[string]cmEntry {
	dir := filepath.Join(root, "CommonModules")
	out := map[string]cmEntry{}
	for lower, name := range dirXMLNames(dir) {
		out[lower] = cmEntry{name: name, xml: filepath.Join(dir, name+".xml")}
	}
	return out
}

// dirXMLNames lists the *.xml files of a directory by lower-cased base name.
// The listing is cached by the directory's modification time: adding or
// removing an object changes it.
func dirXMLNames(dir string) map[string]string {
	st, err := os.Stat(dir)
	if err != nil {
		return nil
	}
	key := "dir\x00" + dir
	if v, ok := cacheGet(key, st.ModTime(), st.Size()); ok {
		return v.(map[string]string)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.EqualFold(filepath.Ext(n), ".xml") {
			continue
		}
		base := strings.TrimSuffix(n, filepath.Ext(n))
		out[strings.ToLower(base)] = base
	}
	cachePut(key, st.ModTime(), st.Size(), out)
	return out
}

// metaInfo is what a metadata object description tells the checks.
type metaInfo struct {
	global  bool
	adopted bool
	// avail: where a common module is compiled (Server, ExternalConnection,
	// ClientManagedApplication).
	avail Availability
	// attrs: top-level attributes and tabular sections, lower -> as written.
	attrs map[string]string
}

// readMetaInfo reads an object's XML description. The common-module form is
// small; object descriptions are read in full, since attributes come last.
func readMetaInfo(path string) metaInfo {
	st, err := os.Stat(path)
	if err != nil {
		return metaInfo{}
	}
	key := "meta\x00" + path
	if v, ok := cacheGet(key, st.ModTime(), st.Size()); ok {
		return v.(metaInfo)
	}
	info := parseMetaInfo(path)
	cachePut(key, st.ModTime(), st.Size(), info)
	return info
}

func parseMetaInfo(path string) metaInfo {
	info := metaInfo{attrs: map[string]string{}}
	f, err := os.Open(path)
	if err != nil {
		return info
	}
	defer f.Close()
	dec := xml.NewDecoder(f)
	dec.Strict = false
	// MetaDataObject/<Type>/Properties/<Prop>
	// MetaDataObject/<Type>/ChildObjects/<Attribute|TabularSection>/Properties/Name
	var stack []string
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF || err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			text.Reset()
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			n := len(stack)
			val := strings.TrimSpace(text.String())
			switch {
			case n == 4 && stack[2] == "Properties" && stack[3] == "Global":
				info.global = val == "true"
			case n == 4 && stack[2] == "Properties" && stack[3] == "ObjectBelonging":
				info.adopted = val == "Adopted"
			case n == 4 && stack[2] == "Properties" && (stack[3] == "Server" || stack[3] == "ExternalConnection"):
				info.avail.Server = info.avail.Server || val == "true"
			case n == 4 && stack[2] == "Properties" && stack[3] == "ClientManagedApplication":
				info.avail.Client = info.avail.Client || val == "true"
			case n == 6 && stack[2] == "ChildObjects" && (stack[3] == "Attribute" || stack[3] == "TabularSection") &&
				stack[4] == "Properties" && stack[5] == "Name":
				info.attrs[strings.ToLower(val)] = val
			}
			if n > 0 {
				stack = stack[:n-1]
			}
			text.Reset()
		}
	}
	return info
}

// readFormAttributes lists the attributes of a form from its Form.xml. Only the
// top-level ones are bare names in the form module; the nested <Attribute> of
// a value-table column sits under <Columns> and is skipped by depth.
func readFormAttributes(path string) (map[string]string, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	key := "form\x00" + path
	if v, ok := cacheGet(key, st.ModTime(), st.Size()); ok {
		return v.(map[string]string), true
	}
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	dec := xml.NewDecoder(f)
	dec.Strict = false
	var stack []string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			if len(stack) == 3 && stack[1] == "Attributes" && t.Name.Local == "Attribute" {
				for _, a := range t.Attr {
					if a.Name.Local == "name" {
						out[strings.ToLower(a.Value)] = a.Value
					}
				}
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	cachePut(key, st.ModTime(), st.Size(), out)
	return out, true
}

// readDecls reads the procedures and functions of a module file. read is
// false when the file does not exist or cannot be read.
func readDecls(path string) (map[string]MethodInfo, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	key := "decl\x00" + path
	if v, ok := cacheGet(key, st.ModTime(), st.Size()); ok {
		return v.(map[string]MethodInfo), true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	out := DeclaredMethods(string(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	cachePut(key, st.ModTime(), st.Size(), out)
	return out, true
}

// DeclaredMethods lists the procedures and functions a module text declares.
func DeclaredMethods(code string) map[string]MethodInfo {
	m := analyze(code)
	out := make(map[string]MethodInfo, len(m.methods))
	for _, md := range m.methods {
		out[strings.ToLower(md.name)] = MethodInfo{Name: md.name, Export: md.export}
	}
	return out
}

func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	k, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:k], nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// fileCache keeps what was parsed from a file until the file changes: the
// server checks drafts all session long, and the configuration's modules do
// not change between calls. Values are names only, a few megabytes for a
// typical configuration.
var fileCache = struct {
	sync.Mutex
	m map[string]cacheEntry
}{m: map[string]cacheEntry{}}

type cacheEntry struct {
	mod  time.Time
	size int64
	v    any
}

func cacheGet(key string, mod time.Time, size int64) (any, bool) {
	fileCache.Lock()
	defer fileCache.Unlock()
	e, ok := fileCache.m[key]
	if !ok || !e.mod.Equal(mod) || e.size != size {
		return nil, false
	}
	return e.v, true
}

func cachePut(key string, mod time.Time, size int64, v any) {
	fileCache.Lock()
	defer fileCache.Unlock()
	fileCache.m[key] = cacheEntry{mod: mod, size: size, v: v}
}
