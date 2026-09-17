package index

import (
	"runtime"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
)

// Config — tunables пайплайна (архитектура §18.5): значения по умолчанию,
// не константы в трёх местах.
type Config struct {
	// DebounceQuiet — тишина изменений, после которой массовая перевыгрузка
	// считается завершённой и публикуется. Default 3с.
	DebounceQuiet time.Duration
	// FallbackMinKeys/FallbackPct — порог fallback re-resolution: превышение
	// max(FallbackMinKeys, FallbackPct*refs компонента) -> полный re-resolve
	// компонента вместо точечного affected set. Default max(5000, 10%).
	FallbackMinKeys int
	FallbackPct     float64
	// SmallChangeFileLimit — граница «мало/много изменений» freshness policy
	// (18.5): не больше -> синхронный инкремент, больше -> фоновый rebuild.
	// Default 50.
	SmallChangeFileLimit int
	// RequireFreshDeadline — на сколько require-fresh готов ждать перед
	// index_not_fresh. Default 10с.
	RequireFreshDeadline time.Duration
	// Workers — размер bounded worker pool парсинга. Default = число ядер.
	Workers int

	// GraphTunables — пороги атрибуции объектного графа (§5, флаги -graph-*).
	// Дефолты живут в internal/resolve (одно место на весь проект): нулевая
	// структура нормализуется там же, поэтому fill() их не подставляет.
	GraphTunables resolve.ObjectEdgeTunables

	// Now подменяет часы в тестах (debounce, freshness age); nil -> time.Now.
	Now func() time.Time
}

// DefaultConfig — значения по умолчанию раздела 18.5.
func DefaultConfig() Config {
	return Config{
		DebounceQuiet:        3 * time.Second,
		FallbackMinKeys:      5000,
		FallbackPct:          0.10,
		SmallChangeFileLimit: 50,
		RequireFreshDeadline: 10 * time.Second,
		Workers:              runtime.NumCPU(),
	}
}

// fill подставляет умолчания на нулевые поля.
func (c *Config) fill() {
	def := DefaultConfig()
	if c.DebounceQuiet <= 0 {
		c.DebounceQuiet = def.DebounceQuiet
	}
	if c.FallbackMinKeys <= 0 {
		c.FallbackMinKeys = def.FallbackMinKeys
	}
	if c.FallbackPct <= 0 {
		c.FallbackPct = def.FallbackPct
	}
	if c.SmallChangeFileLimit <= 0 {
		c.SmallChangeFileLimit = def.SmallChangeFileLimit
	}
	if c.RequireFreshDeadline <= 0 {
		c.RequireFreshDeadline = def.RequireFreshDeadline
	}
	if c.Workers <= 0 {
		c.Workers = def.Workers
	}
}
