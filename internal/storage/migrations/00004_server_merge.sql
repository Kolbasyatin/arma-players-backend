-- +goose Up
-- Журнал схлопываний серверов (ADR 0004). Мягкий merge: история остаётся на исходных записях,
-- канонический сервер — через server.merged_into_server_id.
CREATE TABLE server_merge (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_server_id    bigint      NOT NULL REFERENCES server (id),
    target_server_id    bigint      NOT NULL REFERENCES server (id),
    merged_at           timestamptz NOT NULL DEFAULT now(),
    merged_by           text        NOT NULL,
    reason              text        NOT NULL,
    unmerged_at         timestamptz NULL
);
COMMENT ON TABLE  server_merge                  IS 'Операции схлопывания серверов: кто в кого и почему. Обратимо: unmerged_at + сброс merged_into_server_id.';
COMMENT ON COLUMN server_merge.id               IS 'Внутренний PK.';
COMMENT ON COLUMN server_merge.source_server_id IS 'Запись, которая схлопнута (получила merged_into_server_id).';
COMMENT ON COLUMN server_merge.target_server_id IS 'Канонический сервер, в который слито.';
COMMENT ON COLUMN server_merge.merged_at        IS 'Когда выполнено.';
COMMENT ON COLUMN server_merge.merged_by        IS 'auto — правило (общий ROOM_ID); иначе имя оператора.';
COMMENT ON COLUMN server_merge.reason           IS 'Основание: какие признаки совпали.';
COMMENT ON COLUMN server_merge.unmerged_at      IS 'Если merge отменён — когда. NULL = действует.';
CREATE INDEX server_merge_target_idx ON server_merge (target_server_id);

-- +goose Down
DROP TABLE server_merge;
