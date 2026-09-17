package store

import (
	"context"
	"fmt"
)

// reconcile — шаг (5) write-транзакции инкремента (раздел 15) в двух явных
// действиях: (5a) удаление стабильных identity с пустым UNION аспектов-источников
// и (5b) orphan-node sweep. Повторяется до неподвижной точки: удаление identity
// каскадно сносит дочерние subtype-строки и оставляет их node следующему проходу.
//
// Каскад `DELETE source_file` сносит source-owned SUBTYPE-строки, но их
// node-родители остаются: FK `subtype.id -> node.id ON DELETE CASCADE` работает
// только в направлении node -> subtype (erratum B.6 к ACCEPTED).
func reconcile(ctx context.Context, c *conn) error {
	const maxPasses = 16
	for pass := 0; pass < maxPasses; pass++ {
		changed := false

		// (5a) identity без единого аспекта-источника.
		n, err := c.queryInt(ctx, validateQueries["module_without_aspect"])
		if err != nil {
			return err
		}
		if n > 0 {
			if err := c.exec(ctx, `DELETE FROM module WHERE id IN (
				SELECT m.id FROM module m
				WHERE NOT EXISTS(SELECT 1 FROM module_context x WHERE x.module_id=m.id)
				  AND NOT EXISTS(SELECT 1 FROM module_code    x WHERE x.module_id=m.id))`); err != nil {
				return err
			}
			changed = true
		}
		n, err = c.queryInt(ctx, validateQueries["form_without_aspect"])
		if err != nil {
			return err
		}
		if n > 0 {
			if err := c.exec(ctx, `DELETE FROM form WHERE id IN (
				SELECT f.id FROM form f
				WHERE NOT EXISTS(SELECT 1 FROM form_declaration x WHERE x.form_id=f.id)
				  AND NOT EXISTS(SELECT 1 FROM form_structure   x WHERE x.form_id=f.id))`); err != nil {
				return err
			}
			changed = true
		}

		// (5b) orphan-node sweep, kind-aware.
		n, err = c.queryInt(ctx, validateQueries["orphan_nodes"])
		if err != nil {
			return err
		}
		if n > 0 {
			if err := c.exec(ctx, `DELETE FROM node WHERE id IN (`+orphanNodeSQL("n.id")+`)`); err != nil {
				return err
			}
			changed = true
		}

		if !changed {
			return nil
		}
	}
	return fmt.Errorf("reconciliation не сошлась за %d проходов", maxPasses)
}

// markBlobsFor — атомарный учёт blob (раздел 15) по хэшам, которых коснулась
// текущая транзакция. Полный проход по blob здесь недопустим: WHERE по таблице
// blob поднимает страницы данных всех файлов, а изменить метку может только
// появление или исчезновение ссылки, то есть лишь у затронутых хэшей.
func markBlobsFor(ctx context.Context, c *conn, nowUnix int64, hashes map[string]struct{}) error {
	for h := range hashes {
		if err := c.exec(ctx, `UPDATE blob SET unreferenced_since=?
			WHERE content_hash=? AND unreferenced_since IS NULL
			  AND NOT EXISTS(SELECT 1 FROM source_file f WHERE f.content_hash=blob.content_hash)`,
			nowUnix, h); err != nil {
			return err
		}
		// Повторное появление того же хэша снимает метку (18.2).
		if err := c.exec(ctx, `UPDATE blob SET unreferenced_since=NULL
			WHERE content_hash=? AND unreferenced_since IS NOT NULL
			  AND EXISTS(SELECT 1 FROM source_file f WHERE f.content_hash=blob.content_hash)`,
			h); err != nil {
			return err
		}
	}
	return nil
}

// gcBlobs удаляет blob, метка которых старше TTL и ссылок на которые
// по-прежнему нет. Резюме контракта 18.2: неиспользуемый blob живёт ещё blob_ttl
// ради resource links, и только потом исчезает — ссылка на фрагмент честно
// протухает (`resource_expired`), а не отдаёт другой текст.
func gcBlobs(ctx context.Context, c *conn, nowUnix, ttlSeconds int64) error {
	return c.exec(ctx, `DELETE FROM blob
		WHERE unreferenced_since IS NOT NULL AND unreferenced_since <= ?
		  AND NOT EXISTS(SELECT 1 FROM source_file f WHERE f.content_hash=blob.content_hash)`,
		nowUnix-ttlSeconds)
}
