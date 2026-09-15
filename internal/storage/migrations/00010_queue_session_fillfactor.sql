-- +goose Up
-- Догоняем 00008: player_queue_session обновляется каждым опросом наравне с player_server_session
-- (last_seen_at очереди), но в тот список не попал. Смысл параметров — см. 00008.
ALTER TABLE player_queue_session SET (fillfactor = 85, autovacuum_vacuum_scale_factor = 0.02, autovacuum_vacuum_cost_limit = 1000);

-- +goose Down
ALTER TABLE player_queue_session RESET (fillfactor, autovacuum_vacuum_scale_factor, autovacuum_vacuum_cost_limit);
