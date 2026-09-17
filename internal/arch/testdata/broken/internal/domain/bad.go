// Package domain здесь намеренно сломан: тянет MCP SDK и хранилище.
package domain

import (
	"strings"

	"broken/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Symbol зависит от транспорта — ровно то, что запрещено.
type Symbol struct {
	Tool *mcp.Tool
	Row  store.Row
	Name string
}

// Normalize оставлена, чтобы файл не был одними импортами.
func Normalize(name string) string { return strings.TrimSpace(name) }
