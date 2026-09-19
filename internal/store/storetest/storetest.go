// Package storetest — вспомогательный код для тестов, которым нужен индекс в
// состоянии, недостижимом через публичный API хранилища: файл эпохи
// предыдущей версии схемы, на которой обязана сработать миграция.
//
// Пакет лежит ПОД internal/store намеренно: SQL и драйвер SQLite дальше этого
// поддерева не уходят (RuleSQLOnlyInStore), а тесту в internal/index нужен
// именно мигрируемый индекс, а не имитация признака.
package storetest

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DowngradeToSchema1Statements — обратный ход шага миграции до версии 2:
// снимает таблицы объектного графа и колонку слоя и возвращает
// meta.schema_version к 1. Экспортирован, потому что тестов, которым нужна
// база предыдущей версии, два (в internal/store — на открытой базе, здесь — на
// закрытом файле), а список обязан быть один: две рукописные копии разъедутся,
// и одна из проверок начнёт проверять пустое место.
var DowngradeToSchema1Statements = append(append([]string{}, DowngradeToSchema4Statements...),
	`DROP TABLE IF EXISTS object_data_edge_dep`,
	`DROP TABLE IF EXISTS object_badge`,
	`DROP TABLE IF EXISTS object_data_edge`,
	`ALTER TABLE register_access DROP COLUMN layer`,
	`DELETE FROM meta WHERE key='needs_full_rebuild'`,
	`UPDATE meta SET value='1' WHERE key='schema_version'`,
)

// DowngradeToSchema4Statements: обратный ход шага миграции до версии 5:
// снимает таблицы фактов HTTP (ADR-039). Версию схемы не трогает: её
// выставляет вызывающий, которому нужна конкретная версия 2...4.
var DowngradeToSchema4Statements = []string{
	`DROP TABLE IF EXISTS http_call`,
	`DROP TABLE IF EXISTS http_endpoint`,
}

// DowngradeEpochToSchema1 приводит ЗАКРЫТЫЙ файл эпохи к виду схемы версии 1.
// Следующее store.Open по этому каталогу применит миграцию — тот самый путь,
// которым проходит существующий индекс пользователя после обновления сервера.
func DowngradeEpochToSchema1(epochPath string) error {
	db, err := sql.Open("sqlite", epochPath)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, q := range DowngradeToSchema1Statements {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}
	return nil
}
