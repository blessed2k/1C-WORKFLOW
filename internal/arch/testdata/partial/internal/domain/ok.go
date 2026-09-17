// Package domain — единственный пакет этой фикстуры: остальных ещё нет.
package domain

import "strings"

// Key нормализует имя.
func Key(name string) string { return strings.ToLower(name) }
