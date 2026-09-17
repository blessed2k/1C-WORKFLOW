package workspace_test

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// TestMatchPath закрывает долг таска 02 (interfaces.md, «Долг, переданный из
// таска 02»): MatchPath — единственный матчер include/exclude манифеста, но
// была покрыта только половина-валидатор (splitPattern через ManifestError),
// само сопоставление сегментов не имело ни одного теста. Таблица фиксирует
// семантику ДО того, как index начнёт применять include/exclude при обходе.
func TestMatchPath(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		rel     string
		want    bool
	}{
		{
			name:    "** съедает ноль сегментов",
			pattern: "CommonModules/**/Module.bsl",
			rel:     "CommonModules/Module.bsl",
			want:    true,
		},
		{
			name:    "** съедает несколько сегментов",
			pattern: "CommonModules/**/Module.bsl",
			rel:     "CommonModules/X/Ext/Module.bsl",
			want:    true,
		},
		{
			name:    "* не переходит через слеш",
			pattern: "CommonModules/*/Ext/Module.bsl",
			rel:     "CommonModules/X/Y/Ext/Module.bsl",
			want:    false,
		},
		{
			name:    "* совпадает внутри одного сегмента",
			pattern: "CommonModules/*/Ext/Module.bsl",
			rel:     "CommonModules/X/Ext/Module.bsl",
			want:    true,
		},
		{
			name:    "**/*.bsl из §9 архитектуры совпадает с тестами yaxunit",
			pattern: "**/*.bsl",
			rel:     "tests/yaxunit/ТестыОбмена.bsl",
			want:    true,
		},
		{
			name:    "**/*.bsl совпадает с файлом в корне",
			pattern: "**/*.bsl",
			rel:     "Module.bsl",
			want:    true,
		},
		{
			name:    "точное совпадение без масок",
			pattern: "CommonModules/X/Ext/Module.bsl",
			rel:     "CommonModules/X/Ext/Module.bsl",
			want:    true,
		},
		{
			name:    "несовпадающий сегмент",
			pattern: "CommonModules/X/Ext/Module.bsl",
			rel:     "CommonModules/Y/Ext/Module.bsl",
			want:    false,
		},
		{
			name:    "путь короче шаблона без **",
			pattern: "CommonModules/X/Ext/Module.bsl",
			rel:     "CommonModules/X",
			want:    false,
		},
		{
			name:    "путь длиннее шаблона без **",
			pattern: "CommonModules/X",
			rel:     "CommonModules/X/Ext/Module.bsl",
			want:    false,
		},
		{
			name:    "** в конце шаблона совпадает с любым хвостом, включая пустой",
			pattern: "Tests/**",
			rel:     "Tests",
			want:    true,
		},
		{
			name:    "** в конце шаблона совпадает с вложенным хвостом",
			pattern: "Tests/**",
			rel:     "Tests/yaxunit/Module.bsl",
			want:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := workspace.MatchPath(tc.pattern, tc.rel)
			if err != nil {
				t.Fatalf("MatchPath(%q, %q): %v", tc.pattern, tc.rel, err)
			}
			if got != tc.want {
				t.Errorf("MatchPath(%q, %q) = %v, want %v", tc.pattern, tc.rel, got, tc.want)
			}
		})
	}
}
