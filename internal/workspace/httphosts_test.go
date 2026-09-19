package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadHTTPHosts(t *testing.T) {
	root := t.TempDir()
	m, err := LoadHTTPHosts(root)
	if err != nil || len(m.Hosts) != 0 {
		t.Fatalf("без файла: пустой маппинг без ошибки, получено %+v, %v", m, err)
	}
	if err := os.MkdirAll(filepath.Join(root, RegistryDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"hosts":[
		{"host":"https://ERP.example.local:8443/base","project":"erp"},
		{"host":"*.shop.example.local","project":"shop"},
		{"host":"*.example.local","project":"other"}]}`
	if err := os.WriteFile(HTTPHostsPath(root), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err = LoadHTTPHosts(root)
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]string{
		"erp.example.local":     "erp",
		"eu.shop.example.local": "shop", // длиннейший суффикс сильнее
		"crm.example.local":     "other",
		"shop.example.local":    "other", // «*.shop» не совпадает с самим доменом
		"example.local":         "",
		"partner.example":       "",
	} {
		got, ok := m.Lookup(host)
		if string(got) != want || ok != (want != "") {
			t.Errorf("Lookup(%q) = %q,%v, ожидалось %q", host, got, ok, want)
		}
	}
}

func TestParseHTTPHostsRejectsBrokenFile(t *testing.T) {
	for name, body := range map[string]string{
		"версия":        `{"version":2,"hosts":[]}`,
		"пустой хост":   `{"version":1,"hosts":[{"host":" ","project":"erp"}]}`,
		"пустой проект": `{"version":1,"hosts":[{"host":"erp.local","project":""}]}`,
		"повтор хоста":  `{"version":1,"hosts":[{"host":"erp.local","project":"a"},{"host":"ERP.local","project":"b"}]}`,
		"лишнее поле":   `{"version":1,"hosts":[{"host":"erp.local","project":"a","port":80}]}`,
		"не JSON":       `version: 1`,
	} {
		if _, err := ParseHTTPHosts([]byte(body)); err == nil {
			t.Errorf("%s: ошибка ожидалась", name)
		}
	}
}

func TestMergeHTTPHosts(t *testing.T) {
	a := HTTPHosts{Source: "a", Hosts: []HTTPHostRule{{Host: "erp.local", Project: "erp"}}}
	b := HTTPHosts{Source: "b", Hosts: []HTTPHostRule{{Host: "erp.local", Project: "erp2"}, {Host: "shop.local", Project: "shop"}}}
	m, warnings := MergeHTTPHosts(a, b)
	if p, _ := m.Lookup("erp.local"); p != "erp" {
		t.Errorf("побеждает первый: %q", p)
	}
	if p, _ := m.Lookup("shop.local"); p != "shop" {
		t.Errorf("второй файл дополняет: %q", p)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "erp.local") {
		t.Errorf("расхождение обязано дать предупреждение: %v", warnings)
	}
}

// TestLoadHTTPHostsRejectsOversizedFile: файл больше потолка отвергается с
// ошибкой, а не читается в память целиком.
func TestLoadHTTPHostsRejectsOversizedFile(t *testing.T) {
	root := t.TempDir()
	body := `{"version":1,"hosts":[],"pad":"` + strings.Repeat("x", HTTPHostsMaxBytes) + `"}`
	if err := os.MkdirAll(filepath.Join(root, RegistryDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(HTTPHostsPath(root), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadHTTPHosts(root)
	if err == nil || !strings.Contains(err.Error(), "больше") {
		t.Fatalf("ожидалась ошибка о размере, получено %v", err)
	}
}
