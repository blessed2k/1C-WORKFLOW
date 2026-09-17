package meta

import (
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// parseRoleRights разбирает Roles/<Имя>/Ext/Rights.xml. Rights.xml хранит
// ОТКЛОНЕНИЯ от умолчаний роли: отсутствие объекта в Objects не означает
// «нет доступа», когда включён setForNewObjects — это и есть ИЛИ-логика,
// сохраняемая из internal/source/rightsaudit.go (образец семантики). Факты
// несут ВСЕ права как записаны, включая value=false: эффективное объединение
// прав нескольких ролей — дело слоя резолвера/приложения, не этого пакета.
func parseRoleRights(relPath string, src []byte) (Facts, []domain.Diagnostic) {
	var rights xmlRights
	if diags := unmarshalXML(relPath, src, &rights); diags != nil {
		return Facts{}, diags
	}

	roleName := roleNameFromRightsPath(relPath)
	var diags []domain.Diagnostic
	if roleName == "" {
		diags = append(diags, domain.Diagnostic{
			Code:     DiagEmptyName,
			Severity: domain.SeverityWarning,
			Message:  "не удалось определить имя роли по пути Rights.xml",
			File:     relPath,
		})
	}

	fact := &RoleRightsFact{
		RoleNameNorm:     domain.NormalizeName(roleName),
		RoleNameDisplay:  roleName,
		SetForNewObjects: rights.SetForNewObjects == "true",
	}
	for _, obj := range rights.Objects {
		ro := RoleRightObjectFact{ObjectNameRaw: obj.Name}
		for _, r := range obj.Rights {
			entry := RoleRightEntryFact{Name: r.Name, Value: r.Value == "true"}
			for _, rst := range r.Restrictions {
				cond := strings.TrimSpace(rst.Condition)
				if cond == "" && len(rst.Fields) == 0 {
					continue
				}
				entry.RLS = append(entry.RLS, RLSRestrictionFact{Fields: rst.Fields, Condition: cond})
			}
			ro.Rights = append(ro.Rights, entry)
		}
		fact.Objects = append(fact.Objects, ro)
	}

	return Facts{RoleRights: fact}, diags
}

// roleNameFromRightsPath извлекает имя роли из пути Roles/<Имя>/Ext/Rights.xml:
// Rights.xml не называет свою роль изнутри.
func roleNameFromRightsPath(relPath string) string {
	segs := strings.Split(domain.NormalizeModulePath(relPath), "/")
	if len(segs) != 4 {
		return ""
	}
	return segs[1]
}
