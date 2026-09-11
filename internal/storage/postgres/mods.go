package postgres

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"armaplayers/internal/bohemia"
)

// modSetHash — SHA-1 отсортированного списка modId@version; "" для пустого набора.
// Позволяет за одно сравнение понять, изменился ли набор модов, не трогая server_mod.
func modSetHash(mods []bohemia.Mod) string {
	if len(mods) == 0 {
		return ""
	}
	keys := make([]string, 0, len(mods))
	for _, m := range mods {
		keys = append(keys, m.ModID+"@"+m.Version)
	}
	sort.Strings(keys)
	h := sha1.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// syncServerMods обновляет справочник модов и набор сервера, если хеш набора изменился.
// Возвращает актуальный хеш. При неизменном наборе — ни одного запроса к server_mod.
func syncServerMods(ctx context.Context, tx pgx.Tx, serverID int64, currentHash string, mods []bohemia.Mod, observedAt time.Time) (string, error) {
	newHash := modSetHash(mods)
	if newHash == currentHash {
		return newHash, nil
	}

	batch := &pgx.Batch{}
	for _, m := range mods {
		if m.ModID == "" {
			continue
		}
		batch.Queue(`
			INSERT INTO mod (mod_id, name, first_seen_at, last_seen_at) VALUES ($1, $2, $3, $3)
			ON CONFLICT (mod_id) DO UPDATE SET name = EXCLUDED.name, last_seen_at = GREATEST(mod.last_seen_at, EXCLUDED.last_seen_at)`,
			m.ModID, m.Name, observedAt)
		batch.Queue(`
			INSERT INTO server_mod (server_id, mod_id, version, first_seen_at, last_seen_at) VALUES ($1, $2, $3, $4, $4)
			ON CONFLICT (server_id, mod_id, version) DO UPDATE
			   SET last_seen_at = GREATEST(server_mod.last_seen_at, EXCLUDED.last_seen_at),
			       observation_count = server_mod.observation_count + 1`,
			serverID, m.ModID, m.Version, observedAt)
	}
	batch.Queue(`UPDATE server SET mod_set_hash = NULLIF($2, ''), updated_at = now() WHERE id = $1`, serverID, newHash)
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return "", fmt.Errorf("sync mods: %w", err)
	}
	return newHash, nil
}
