package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	// Единственная внешняя зависимость хранилища: pure-Go драйвер SQLite
	// поверх database/sql (ADR-2, решение принято). CGO здесь недопустим.
	_ "modernc.org/sqlite"
)

const driverName = "sqlite"

// conn — ОДНО выделенное соединение с базой эпохи.
//
// database/sql требует именно выделенного *sql.Conn (ADR-2 §7): пул соединений
// несовместим ни с pragma-на-соединение, ни с явными BEGIN/COMMIT — следующий
// запрос мог бы уехать на другое соединение, где нет ни транзакции, ни
// foreign_keys. Поэтому у каждого conn ровно одно физическое соединение.
type conn struct {
	db   *sql.DB
	sc   *sql.Conn
	path string
}

// createConn создаёт НОВЫЙ файл БД: применимы creation-only и persistent pragma.
func createConn(ctx context.Context, path string, busyTimeoutMillis int) (*conn, error) {
	return openWithPragmas(ctx, path, createPragmaList(busyTimeoutMillis))
}

// openConn открывает СУЩЕСТВУЮЩИЙ файл БД: применимы только per-connection
// pragma, persistent — проверяются. Этим же путём пул открывает читателей.
func openConn(ctx context.Context, path string, busyTimeoutMillis int) (*conn, error) {
	return openWithPragmas(ctx, path, openPragmaList(busyTimeoutMillis))
}

func openWithPragmas(ctx context.Context, path string, pragmas []string) (*conn, error) {
	db, err := sql.Open(driverName, dsn(path))
	if err != nil {
		return nil, fmt.Errorf("открытие %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	sc, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("выделенное соединение к %s: %w", path, err)
	}
	c := &conn{db: db, sc: sc, path: path}
	for _, p := range pragmas {
		if err := c.exec(ctx, p); err != nil {
			c.Close()
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	if err := verifyPersistentPragmas(ctx, c); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// dsn собирает DSN драйвера. Путь экранируется как часть URI: пробелы и
// кириллица в пути допустимы на обеих целевых платформах.
func dsn(path string) string {
	return "file:" + url.PathEscape(path)
}

func (c *conn) exec(ctx context.Context, sqlText string, args ...any) error {
	_, err := c.sc.ExecContext(ctx, sqlText, args...)
	return err
}

// execInsert выполняет вставку и отдаёт присвоенный rowid.
func (c *conn) execInsert(ctx context.Context, sqlText string, args ...any) (int64, error) {
	res, err := c.sc.ExecContext(ctx, sqlText, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (c *conn) queryInt(ctx context.Context, sqlText string, args ...any) (int64, error) {
	var v sql.NullInt64
	err := c.sc.QueryRowContext(ctx, sqlText, args...).Scan(&v)
	return v.Int64, err
}

func (c *conn) queryText(ctx context.Context, sqlText string, args ...any) (string, error) {
	var v sql.NullString
	err := c.sc.QueryRowContext(ctx, sqlText, args...).Scan(&v)
	return v.String, err
}

func (c *conn) queryBlob(ctx context.Context, sqlText string, args ...any) ([]byte, error) {
	var v []byte
	err := c.sc.QueryRowContext(ctx, sqlText, args...).Scan(&v)
	return v, err
}

func (c *conn) query(ctx context.Context, sqlText string, args ...any) (*sql.Rows, error) {
	return c.sc.QueryContext(ctx, sqlText, args...)
}

// checkpoint отдаёт все три колонки PRAGMA wal_checkpoint: busy, размер WAL в
// страницах и сколько страниц перенесено. Без busy и остатка WAL нельзя понять,
// удалось ли забрать WAL целиком, а публиковать эпоху с непустым WAL запрещено.
func (c *conn) checkpoint(ctx context.Context, mode string) (busy, walPages, moved int64, err error) {
	err = c.sc.QueryRowContext(ctx, "PRAGMA wal_checkpoint("+mode+")").Scan(&busy, &walPages, &moved)
	return
}

func (c *conn) Close() error {
	err := c.sc.Close()
	if e := c.db.Close(); err == nil {
		err = e
	}
	return err
}

// applySchema создаёт схему и заполняет meta. Индексы строятся тем же вызовом:
// схема эпохи собирается один раз и целиком.
func applySchema(ctx context.Context, c *conn, epoch int) error {
	if err := execScript(ctx, c, createScript); err != nil {
		return err
	}
	if err := execScript(ctx, c, indexScript); err != nil {
		return err
	}
	for _, kv := range [][2]string{
		{metaSchemaVersion, itoa(SchemaVersion)},
		{metaEpoch, itoa(epoch)},
		{metaCurrentGeneration, "1"},
	} {
		if err := c.exec(ctx, `INSERT INTO meta(key,value) VALUES(?,?)`, kv[0], kv[1]); err != nil {
			return fmt.Errorf("meta %s: %w", kv[0], err)
		}
	}
	return nil
}

func execScript(ctx context.Context, c *conn, script string) error {
	for _, stmt := range splitStatements(script) {
		if err := c.exec(ctx, stmt); err != nil {
			return fmt.Errorf("%.60s...: %w", stmt, err)
		}
	}
	return nil
}

// splitStatements режет DDL по ';'. Комментарии снимаются ПЕРВЫМИ: точка с
// запятой внутри комментария иначе разрезает оператор посреди фразы, и ошибка
// вылезает как невнятное «syntax error». Строковых литералов с ';' и триггеров
// в схеме нет, поэтому этого достаточно.
func splitStatements(script string) []string {
	var out []string
	for _, part := range strings.Split(stripSQLComments(script), ";") {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func stripSQLComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
