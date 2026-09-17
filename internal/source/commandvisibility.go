package source

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CommandVisibility answers what a user with a given set of roles actually sees
// in a section. In configurations where every role grants rights on the objects
// anyway (BSP-based ones usually do), the real access boundary is not rights at
// all — it is command visibility in CommandInterface.xml, where each command
// carries a default plus per-role overrides.
type CommandVisibility struct {
	Subsystem string           `json:"subsystem,omitempty" jsonschema:"subsystem the commands belong to"`
	Role      string           `json:"role,omitempty" jsonschema:"role the answer was filtered for"`
	Commands  []CommandVisible `json:"commands"`
	Count     int              `json:"count"`
	Roles     []string         `json:"rolesMentioned,omitempty" jsonschema:"every role named in the scanned files: these are the ones visibility is actually tuned for"`
	Note      string           `json:"note,omitempty"`
}

// CommandVisible is one command and how it is shown.
type CommandVisible struct {
	Command   string            `json:"command" jsonschema:"e.g. CommonCommand.ПанельОтчетовCRMИМаркетинг"`
	Subsystem string            `json:"subsystem"`
	Common    string            `json:"common" jsonschema:"default visibility: true, false or unset"`
	ByRole    map[string]string `json:"byRole,omitempty" jsonschema:"per-role overrides of the default"`
	File      string            `json:"file" jsonschema:"CommandInterface.xml the entry came from"`
	// Visible is filled only when a role was asked about: what that role sees,
	// which is the override if there is one and the default otherwise.
	Visible string `json:"visible,omitempty" jsonschema:"what the requested role sees: true or false"`
}

type xmlCommandInterface struct {
	CommandsVisibility struct {
		Commands []struct {
			Name       string `xml:"name,attr"`
			Visibility struct {
				Common string `xml:"Common"`
				Values []struct {
					Name  string `xml:"name,attr"`
					Value string `xml:",chardata"`
				} `xml:"Value"`
			} `xml:"Visibility"`
		} `xml:"Command"`
	} `xml:"CommandsVisibility"`
}

// commandVisibilityLimit caps the listing. A production configuration has over a
// thousand commands with tuned visibility, which is a wall of text rather than an
// answer; the count plus a filtered slice is what the caller can act on.
const commandVisibilityLimit = 200

// CommandVisibilityAudit scans the command interfaces of the export. Every filter
// is optional: with a role it says what that role sees, and rolesOnly keeps just
// the commands that carry per-role overrides — the ones access is actually tuned
// with.
func (s *XMLSource) CommandVisibilityAudit(_ context.Context, subsystem, role string, rolesOnly bool) (*CommandVisibility, error) {
	out := &CommandVisibility{Subsystem: subsystem, Role: role, Commands: []CommandVisible{}}
	wantSub := strings.ToLower(strings.TrimSpace(subsystem))
	wantRole := normalizeRoleName(role)
	mentioned := map[string]bool{}

	root := filepath.Join(s.root, "Subsystems")
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.EqualFold(d.Name(), "CommandInterface.xml") {
			return nil
		}
		sub := subsystemOfPath(s.root, path)
		if wantSub != "" && !strings.EqualFold(sub, subsystem) {
			return nil
		}

		var ci xmlCommandInterface
		if err := readXML(path, &ci); err != nil {
			return nil // a malformed interface file must not sink the whole scan
		}
		rel, _ := filepath.Rel(s.root, path)
		rel = filepath.ToSlash(rel)

		for _, c := range ci.CommandsVisibility.Commands {
			entry := CommandVisible{
				Command:   c.Name,
				Subsystem: sub,
				Common:    strings.TrimSpace(c.Visibility.Common),
				File:      rel,
			}
			for _, v := range c.Visibility.Values {
				name := normalizeRoleName(v.Name)
				if name == "" {
					continue
				}
				if entry.ByRole == nil {
					entry.ByRole = map[string]string{}
				}
				entry.ByRole[name] = strings.TrimSpace(v.Value)
				mentioned[name] = true
			}
			if wantRole != "" {
				entry.Visible = visibleForRole(entry, wantRole)
			}
			// A command with neither a default nor overrides carries no signal.
			if entry.Common == "" && len(entry.ByRole) == 0 {
				continue
			}
			if rolesOnly && len(entry.ByRole) == 0 {
				continue
			}
			out.Commands = append(out.Commands, entry)
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	sort.Slice(out.Commands, func(i, j int) bool {
		if out.Commands[i].Subsystem != out.Commands[j].Subsystem {
			return out.Commands[i].Subsystem < out.Commands[j].Subsystem
		}
		return out.Commands[i].Command < out.Commands[j].Command
	})
	out.Count = len(out.Commands)
	for r := range mentioned {
		out.Roles = append(out.Roles, r)
	}
	sort.Strings(out.Roles)

	switch {
	case out.Count == 0:
		out.Note = "команд с настроенной видимостью не найдено; проверьте имя подсистемы через get_metadata_tree type=Subsystem"
	case role != "":
		out.Note = fmt.Sprintf("visible=true — команда видна роли %s (переопределение роли важнее общего значения)", role)
	}
	if len(out.Commands) > commandVisibilityLimit {
		out.Commands = out.Commands[:commandVisibilityLimit]
		out.Note = appendNote(out.Note, fmt.Sprintf(
			"показаны %d команд из %d — сузьте subsystem= или оставьте rolesOnly=true",
			commandVisibilityLimit, out.Count))
	}
	return out, nil
}

// appendNote joins notes without losing the earlier one.
func appendNote(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

// visibleForRole resolves what one role sees: its own override wins over the
// common default.
func visibleForRole(c CommandVisible, role string) string {
	if v, ok := c.ByRole[role]; ok {
		return v
	}
	if c.Common == "" {
		return "true" // no default recorded means the platform default, visible
	}
	return c.Common
}

// normalizeRoleName accepts "Role.X", "Роль.X" or a bare name and returns the
// bare name, so callers do not have to know the export's spelling.
func normalizeRoleName(name string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		return ""
	}
	for _, prefix := range []string{"Role.", "Роль."} {
		if strings.HasPrefix(n, prefix) {
			return n[len(prefix):]
		}
	}
	return n
}

// subsystemOfPath recovers the subsystem name from a CommandInterface.xml path,
// including nested ones (Subsystems/A/Subsystems/B/Ext/CommandInterface.xml).
func subsystemOfPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	name := ""
	for i, p := range parts {
		if p == "Subsystems" && i+1 < len(parts) {
			name = parts[i+1]
		}
	}
	return name
}
