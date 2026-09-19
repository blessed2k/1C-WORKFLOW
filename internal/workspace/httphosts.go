package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Маппинг хостов на проекты (веха В2, решение D10 в
// docs/architecture-graph.md, ADR-039): какой проект workspace стоит за сервером в
// Новый HTTPСоединение("...").
// Файл лежит рядом с реестром и индексами, в <workspace>/.mcp1c/http-hosts.json:
//
//	{
//	  "version": 1,
//	  "hosts": [
//	    {"host": "erp.example.local", "project": "erp"},
//	    {"host": "*.shop.example.local", "project": "shop"}
//	  ]
//	}
//
// host: имя сервера без схемы, порта и пути, без учёта регистра; "*." в
// начале совпадает с любым поддоменом (но не с самим доменом). project: id
// проекта из реестра (registry.json, 1c-project.json "project"). Файла нет :
// маппинг пуст, и все вызовы с литеральным хостом видны как внешние. Сшивка
// читает файл при каждом запросе: правка маппинга не требует переиндексации.

const (
	// HTTPHostsFileName: файл маппинга хостов в RegistryDirName.
	HTTPHostsFileName = "http-hosts.json"
	// HTTPHostsVersion: версия формата маппинга.
	HTTPHostsVersion = 1
	// HTTPHostsMaxBytes: потолок чтения файла маппинга. Маппинг это
	// десятки строк; файл больше потолка отвергается, а не читается в память.
	HTTPHostsMaxBytes = 1 << 20
)

// HTTPHostRule: одно правило маппинга.
type HTTPHostRule struct {
	Host    string           `json:"host"`
	Project domain.ProjectID `json:"project"`
}

// HTTPHosts: маппинг хостов на проекты.
type HTTPHosts struct {
	Version int            `json:"version"`
	Hosts   []HTTPHostRule `json:"hosts"`
	// Source: откуда прочитан (для сообщений); пусто у пустого маппинга.
	Source string `json:"-"`
}

// HTTPHostsPath: путь файла маппинга в workspace.
func HTTPHostsPath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, RegistryDirName, HTTPHostsFileName)
}

// LoadHTTPHosts читает маппинг workspace. Отсутствующий файл: пустой
// маппинг без ошибки; нечитаемый или неверный: ошибка с именем файла:
// молча пустой маппинг выдал бы все вызовы за внешние.
func LoadHTTPHosts(workspaceRoot string) (HTTPHosts, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		return HTTPHosts{Version: HTTPHostsVersion}, nil
	}
	path := HTTPHostsPath(workspaceRoot)
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return HTTPHosts{Version: HTTPHostsVersion}, nil
	}
	if err != nil {
		return HTTPHosts{}, fmt.Errorf("маппинг хостов %s: %w", path, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, HTTPHostsMaxBytes+1))
	if err != nil {
		return HTTPHosts{}, fmt.Errorf("маппинг хостов %s: %w", path, err)
	}
	if len(raw) > HTTPHostsMaxBytes {
		return HTTPHosts{}, fmt.Errorf("маппинг хостов %s: файл больше %d байт", path, HTTPHostsMaxBytes)
	}
	m, err := ParseHTTPHosts(raw)
	if err != nil {
		return HTTPHosts{}, fmt.Errorf("маппинг хостов %s: %w", path, err)
	}
	m.Source = path
	return m, nil
}

// ParseHTTPHosts разбирает и проверяет содержимое файла маппинга.
func ParseHTTPHosts(raw []byte) (HTTPHosts, error) {
	var m HTTPHosts
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return HTTPHosts{}, fmt.Errorf("не разбирается: %w", err)
	}
	if m.Version != HTTPHostsVersion {
		return HTTPHosts{}, fmt.Errorf("version=%d, поддерживается %d", m.Version, HTTPHostsVersion)
	}
	seen := map[string]domain.ProjectID{}
	for i, r := range m.Hosts {
		host := normalizeHostPattern(r.Host)
		if host == "" || host == "*." || strings.Contains(host, "*") && !strings.HasPrefix(host, "*.") {
			return HTTPHosts{}, fmt.Errorf("hosts[%d]: пустой или неверный host %q (звёздочка допустима только как «*.» в начале)", i, r.Host)
		}
		if strings.TrimSpace(string(r.Project)) == "" {
			return HTTPHosts{}, fmt.Errorf("hosts[%d] (%s): пустой project", i, r.Host)
		}
		if prev, dup := seen[host]; dup {
			return HTTPHosts{}, fmt.Errorf("hosts[%d]: хост %s уже сопоставлен проекту %s", i, host, prev)
		}
		seen[host] = r.Project
		m.Hosts[i].Host = host
	}
	return m, nil
}

// Lookup: проект нормализованного хоста. Точное правило сильнее шаблона,
// из шаблонов побеждает самый длинный суффикс.
func (m HTTPHosts) Lookup(host string) (domain.ProjectID, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", false
	}
	var best HTTPHostRule
	bestLen := -1
	for _, r := range m.Hosts {
		if r.Host == host {
			return r.Project, true
		}
		if suffix, ok := strings.CutPrefix(r.Host, "*"); ok && strings.HasSuffix(host, suffix) &&
			len(host) > len(suffix) && len(suffix) > bestLen {
			best, bestLen = r, len(suffix)
		}
	}
	if bestLen < 0 {
		return "", false
	}
	return best.Project, true
}

// MergeHTTPHosts сливает маппинги нескольких workspace (карта с несколькими
// --project): при одном хосте в разных файлах побеждает первый по порядку, а
// расхождение возвращается строкой предупреждения, не теряется.
func MergeHTTPHosts(maps ...HTTPHosts) (HTTPHosts, []string) {
	out := HTTPHosts{Version: HTTPHostsVersion}
	owner := map[string]HTTPHostRule{}
	from := map[string]string{}
	var warnings []string
	for _, m := range maps {
		for _, r := range m.Hosts {
			if prev, dup := owner[r.Host]; dup {
				if prev.Project != r.Project {
					warnings = append(warnings, fmt.Sprintf("хост %s: %s сопоставляет его проекту %s, %s проекту %s; действует первое",
						r.Host, from[r.Host], prev.Project, m.Source, r.Project))
				}
				continue
			}
			owner[r.Host], from[r.Host] = r, m.Source
			out.Hosts = append(out.Hosts, r)
		}
	}
	return out, warnings
}

// normalizeHostPattern приводит правило к виду сравнения тем же правилом,
// что и сервер вызова (domain.NormalizeHTTPHost); «*.домен» сохраняет
// звёздочку.
func normalizeHostPattern(raw string) string {
	raw = strings.TrimSpace(raw)
	if rest, ok := strings.CutPrefix(raw, "*."); ok {
		return "*." + domain.NormalizeHTTPHost(rest)
	}
	return domain.NormalizeHTTPHost(raw)
}
