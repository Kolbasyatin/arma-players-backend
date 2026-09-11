-- +goose Up
-- Всё, что приходит в rooms/search, должно жить в нормализованных таблицах, а не только в raw_payload:
-- сырьё хранится недолго и может быть отключено.

-- Справочник модов: modId из Workshop → последнее известное имя.
CREATE TABLE mod (
    mod_id          text        PRIMARY KEY,
    name            text        NOT NULL,
    first_seen_at   timestamptz NOT NULL,
    last_seen_at    timestamptz NOT NULL
);
COMMENT ON TABLE  mod               IS 'Моды Arma Reforger, встреченные в списках серверов. Ключ — modId из Workshop (16 hex).';
COMMENT ON COLUMN mod.mod_id        IS 'Идентификатор мода в Workshop.';
COMMENT ON COLUMN mod.name          IS 'Последнее наблюдённое имя мода.';
COMMENT ON COLUMN mod.first_seen_at IS 'Первое наблюдение на любом сервере.';
COMMENT ON COLUMN mod.last_seen_at  IS 'Последнее наблюдение.';

-- Набор модов сервера с историей: какая версия какого мода и когда стояла.
CREATE TABLE server_mod (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    server_id           bigint      NOT NULL REFERENCES server (id),
    mod_id              text        NOT NULL REFERENCES mod (mod_id),
    version             text        NOT NULL,
    first_seen_at       timestamptz NOT NULL,
    last_seen_at        timestamptz NOT NULL,
    observation_count   integer     NOT NULL DEFAULT 1,
    UNIQUE (server_id, mod_id, version)
);
COMMENT ON TABLE  server_mod                   IS 'Какие моды (и версии) стояли на сервере и когда. Обновляется только при изменении набора (по mod_set_hash), поэтому строка = период жизни версии мода на сервере.';
COMMENT ON COLUMN server_mod.id                IS 'Внутренний PK.';
COMMENT ON COLUMN server_mod.server_id         IS 'Сервер.';
COMMENT ON COLUMN server_mod.mod_id            IS 'Мод.';
COMMENT ON COLUMN server_mod.version           IS 'Версия мода на сервере.';
COMMENT ON COLUMN server_mod.first_seen_at     IS 'Первое наблюдение этой версии на сервере.';
COMMENT ON COLUMN server_mod.last_seen_at      IS 'Последнее наблюдение.';
COMMENT ON COLUMN server_mod.observation_count IS 'Сколько раз набор с этой версией фиксировался (при смене набора).';
CREATE INDEX server_mod_mod_idx ON server_mod (mod_id);

-- Текущий набор модов сервера как хеш: дешёвое сравнение при каждом наблюдении.
ALTER TABLE server ADD COLUMN mod_set_hash text NULL;
COMMENT ON COLUMN server.mod_set_hash IS 'SHA-1 отсортированного списка modId@version; NULL — модов нет или ещё не наблюдали.';

-- Поля ответа rooms/search, которых не хватало в снимке.
ALTER TABLE server_observation
    ADD COLUMN mod_count                  integer NULL,
    ADD COLUMN mod_set_hash               text    NULL,
    ADD COLUMN hosted_scenario_mod_id     text    NULL,
    ADD COLUMN supported_game_client_types text[] NULL,
    ADD COLUMN queue_type                 text    NULL,
    ADD COLUMN flags                      integer NULL,
    ADD COLUMN last_joined_at             timestamptz NULL;
COMMENT ON COLUMN server_observation.mod_count                   IS 'Число модов в наборе на момент снимка.';
COMMENT ON COLUMN server_observation.mod_set_hash                IS 'Хеш набора модов; сам набор — в server_mod.';
COMMENT ON COLUMN server_observation.hosted_scenario_mod_id      IS 'modId мода, содержащего сценарий; пусто для ванильных сценариев.';
COMMENT ON COLUMN server_observation.supported_game_client_types IS 'Платформы, которым доступен сервер: PLATFORM_PC, PLATFORM_PSN, PLATFORM_XBL.';
COMMENT ON COLUMN server_observation.queue_type                  IS 'Тип очереди из joinQueue.type; наблюдалось REGULAR.';
COMMENT ON COLUMN server_observation.flags                       IS 'Битовая маска flags из ответа; значение бит не исследовано.';
COMMENT ON COLUMN server_observation.last_joined_at              IS 'Поле lastJoinedAt из ответа (unix → timestamptz); не у всех комнат.';

-- +goose Down
ALTER TABLE server_observation
    DROP COLUMN mod_count, DROP COLUMN mod_set_hash, DROP COLUMN hosted_scenario_mod_id,
    DROP COLUMN supported_game_client_types, DROP COLUMN queue_type, DROP COLUMN flags, DROP COLUMN last_joined_at;
ALTER TABLE server DROP COLUMN mod_set_hash;
DROP TABLE server_mod;
DROP TABLE mod;
