-- +goose Up
-- Ник, под которым игрок был виден в этой сессии: отвечает на вопрос «на каком сервере какой ник».
-- Обновляется каждым успешным poll, пока сессия открыта; после закрытия — последний ник визита.
ALTER TABLE player_server_session ADD COLUMN nickname text NOT NULL DEFAULT '';
COMMENT ON COLUMN player_server_session.nickname IS 'Ник игрока в этом визите (последний наблюдённый до закрытия сессии).';
ALTER TABLE player_queue_session ADD COLUMN nickname text NOT NULL DEFAULT '';
COMMENT ON COLUMN player_queue_session.nickname IS 'Ник игрока во время этого ожидания в очереди.';
CREATE INDEX player_server_session_nickname_idx ON player_server_session (lower(nickname));

-- +goose Down
DROP INDEX player_server_session_nickname_idx;
ALTER TABLE player_queue_session DROP COLUMN nickname;
ALTER TABLE player_server_session DROP COLUMN nickname;
