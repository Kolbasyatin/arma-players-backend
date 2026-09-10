-- +goose Up

-- ---------------------------------------------------------------------------
-- server: откуда взялся флаг tracking_enabled (ADR 0008).
-- ---------------------------------------------------------------------------
ALTER TABLE server ADD COLUMN tracking_source text NULL;
COMMENT ON COLUMN server.tracking_source IS 'Почему сервер отслеживается: MANUAL (список в конфиге) | AUTO (правило по онлайну). NULL при tracking_enabled=false.';

-- ---------------------------------------------------------------------------
-- player_identity: игрок = Bohemia userId (ADR 0006).
-- ---------------------------------------------------------------------------
CREATE TABLE player_identity (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    bohemia_user_id     uuid        NOT NULL UNIQUE,
    current_nickname    text        NOT NULL,
    first_seen_at       timestamptz NOT NULL,
    last_seen_at        timestamptz NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE  player_identity                  IS 'Игрок. Единственный UNIQUE — Bohemia userId из listPlayers. Ник и платформенные id — атрибуты с историей в соседних таблицах.';
COMMENT ON COLUMN player_identity.id               IS 'Внутренний PK; на него ссылаются сессии, алиасы, события.';
COMMENT ON COLUMN player_identity.bohemia_user_id  IS 'userId из listPlayers (UUID). Основной внешний идентификатор для всех платформ.';
COMMENT ON COLUMN player_identity.current_nickname IS 'Последний наблюдённый ник. История — player_alias.';
COMMENT ON COLUMN player_identity.first_seen_at    IS 'Первое наблюдение игрока на любом отслеживаемом сервере.';
COMMENT ON COLUMN player_identity.last_seen_at     IS 'Последнее наблюдение (в игре или в очереди).';
COMMENT ON COLUMN player_identity.created_at       IS 'Создание строки.';
COMMENT ON COLUMN player_identity.updated_at       IS 'Последнее изменение строки.';

-- ---------------------------------------------------------------------------
-- player_platform_identity: платформенные id (SteamID64 / PSN / XBL) с историей.
-- ---------------------------------------------------------------------------
CREATE TABLE player_platform_identity (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    player_id           bigint      NOT NULL REFERENCES player_identity (id),
    game_client_type    text        NOT NULL,
    platform_user_id    text        NOT NULL,
    first_seen_at       timestamptz NOT NULL,
    last_seen_at        timestamptz NOT NULL,
    observation_count   integer     NOT NULL DEFAULT 1,
    UNIQUE (player_id, game_client_type, platform_user_id)
);
COMMENT ON TABLE  player_platform_identity                   IS 'Связь игрока с платформенным аккаунтом. Уникальность только внутри игрока: один platform_user_id у двух Bohemia userId — аномалия, которую надо видеть, а не отвергать.';
COMMENT ON COLUMN player_platform_identity.id                IS 'Внутренний PK.';
COMMENT ON COLUMN player_platform_identity.player_id         IS 'Игрок.';
COMMENT ON COLUMN player_platform_identity.game_client_type  IS 'PLATFORM_PC | PLATFORM_PSN | PLATFORM_XBL (наблюдались все три).';
COMMENT ON COLUMN player_platform_identity.platform_user_id  IS 'Для PLATFORM_PC — SteamID64; для PSN — числовой id; для XBL — 40 hex-символов. Хранится как текст.';
COMMENT ON COLUMN player_platform_identity.first_seen_at     IS 'Первое наблюдение маппинга.';
COMMENT ON COLUMN player_platform_identity.last_seen_at      IS 'Последнее наблюдение.';
COMMENT ON COLUMN player_platform_identity.observation_count IS 'Сколько раз маппинг наблюдался.';
CREATE INDEX player_platform_identity_lookup_idx ON player_platform_identity (game_client_type, platform_user_id);

-- ---------------------------------------------------------------------------
-- player_alias: история ников.
-- ---------------------------------------------------------------------------
CREATE TABLE player_alias (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    player_id           bigint      NOT NULL REFERENCES player_identity (id),
    nickname            text        NOT NULL,
    first_seen_at       timestamptz NOT NULL,
    last_seen_at        timestamptz NOT NULL,
    observation_count   integer     NOT NULL DEFAULT 1,
    UNIQUE (player_id, nickname)
);
COMMENT ON TABLE  player_alias                   IS 'Какие ники использовал игрок и когда. Ник не идентичность: меняется и не уникален между игроками.';
COMMENT ON COLUMN player_alias.id                IS 'Внутренний PK.';
COMMENT ON COLUMN player_alias.player_id         IS 'Игрок.';
COMMENT ON COLUMN player_alias.nickname          IS 'Ник как пришёл из API (username).';
COMMENT ON COLUMN player_alias.first_seen_at     IS 'Первое наблюдение ника у игрока.';
COMMENT ON COLUMN player_alias.last_seen_at      IS 'Последнее наблюдение.';
COMMENT ON COLUMN player_alias.observation_count IS 'Сколько раз наблюдался.';
CREATE INDEX player_alias_nickname_idx ON player_alias (lower(nickname));

-- ---------------------------------------------------------------------------
-- player_server_session: присутствие на сервере с интервалом неопределённости (ADR 0005).
-- ---------------------------------------------------------------------------
CREATE TABLE player_server_session (
    id                      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    player_id               bigint      NOT NULL REFERENCES player_identity (id),
    server_id               bigint      NOT NULL REFERENCES server (id),
    first_seen_at           timestamptz NOT NULL,
    last_seen_at            timestamptz NOT NULL,
    first_known_absent_at   timestamptz NULL,
    ended_at                timestamptz NULL,
    status                  text        NOT NULL,
    absent_polls            integer     NOT NULL DEFAULT 0,
    startup_replay          boolean     NOT NULL DEFAULT false,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE  player_server_session                       IS 'Один визит игрока на сервер. Истинное время выхода неизвестно: оно между last_seen_at и first_known_absent_at.';
COMMENT ON COLUMN player_server_session.id                    IS 'Внутренний PK.';
COMMENT ON COLUMN player_server_session.player_id             IS 'Игрок.';
COMMENT ON COLUMN player_server_session.server_id             IS 'Сервер (исходная запись; канонический — через merged_into_server_id).';
COMMENT ON COLUMN player_server_session.first_seen_at         IS 'Первый успешный poll, где игрок присутствовал.';
COMMENT ON COLUMN player_server_session.last_seen_at          IS 'Последний успешный poll, где игрок присутствовал.';
COMMENT ON COLUMN player_server_session.first_known_absent_at IS 'Первый успешный poll, где игрока не было. NULL пока присутствует.';
COMMENT ON COLUMN player_server_session.ended_at              IS 'Момент закрытия сессии: first_known_absent_at при подтверждённом выходе, last_seen_at при разрыве данных.';
COMMENT ON COLUMN player_server_session.status                IS 'ONLINE | SUSPECTED_GONE | CLOSED_LEFT | CLOSED_DATA_GAP.';
COMMENT ON COLUMN player_server_session.absent_polls          IS 'Сколько успешных poll подряд игрок отсутствовал. Сбрасывается при появлении.';
COMMENT ON COLUMN player_server_session.startup_replay        IS 'Сессия открыта первым poll после старта процесса: игрок мог быть на сервере давно.';
COMMENT ON COLUMN player_server_session.created_at            IS 'Создание строки.';
COMMENT ON COLUMN player_server_session.updated_at            IS 'Последнее изменение строки.';
CREATE INDEX player_server_session_open_idx   ON player_server_session (server_id) WHERE ended_at IS NULL;
CREATE INDEX player_server_session_player_idx ON player_server_session (player_id, first_seen_at DESC);
CREATE INDEX player_server_session_server_time_idx ON player_server_session (server_id, first_seen_at DESC);

-- ---------------------------------------------------------------------------
-- player_queue_session: ожидание в очереди на вход.
-- ---------------------------------------------------------------------------
CREATE TABLE player_queue_session (
    id                      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    player_id               bigint      NOT NULL REFERENCES player_identity (id),
    server_id               bigint      NOT NULL REFERENCES server (id),
    first_seen_at           timestamptz NOT NULL,
    last_seen_at            timestamptz NOT NULL,
    first_known_absent_at   timestamptz NULL,
    ended_at                timestamptz NULL,
    status                  text        NOT NULL,
    absent_polls            integer     NOT NULL DEFAULT 0,
    result                  text        NULL,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE  player_queue_session                       IS 'Одно ожидание игрока в очереди сервера. Отдельно от присутствия внутри.';
COMMENT ON COLUMN player_queue_session.id                    IS 'Внутренний PK.';
COMMENT ON COLUMN player_queue_session.player_id             IS 'Игрок.';
COMMENT ON COLUMN player_queue_session.server_id             IS 'Сервер.';
COMMENT ON COLUMN player_queue_session.first_seen_at         IS 'Первый poll с игроком в queuePlayers.';
COMMENT ON COLUMN player_queue_session.last_seen_at          IS 'Последний poll с игроком в очереди.';
COMMENT ON COLUMN player_queue_session.first_known_absent_at IS 'Первый poll без игрока в очереди.';
COMMENT ON COLUMN player_queue_session.ended_at              IS 'Момент закрытия.';
COMMENT ON COLUMN player_queue_session.status                IS 'ONLINE | SUSPECTED_GONE | CLOSED_LEFT | CLOSED_DATA_GAP (те же состояния, что у присутствия).';
COMMENT ON COLUMN player_queue_session.absent_polls          IS 'Подряд успешных poll без игрока в очереди.';
COMMENT ON COLUMN player_queue_session.result                IS 'Итог: JOINED_SERVER (появился в connected) | LEFT_QUEUE | UNKNOWN (разрыв данных). NULL пока открыта.';
COMMENT ON COLUMN player_queue_session.created_at            IS 'Создание строки.';
COMMENT ON COLUMN player_queue_session.updated_at            IS 'Последнее изменение строки.';
CREATE INDEX player_queue_session_open_idx ON player_queue_session (server_id) WHERE ended_at IS NULL;

-- ---------------------------------------------------------------------------
-- domain_event: derived-события, outbox (ADR 0007).
-- ---------------------------------------------------------------------------
CREATE TABLE domain_event (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_type      text        NOT NULL,
    occurred_at     timestamptz NOT NULL,
    player_id       bigint      NULL REFERENCES player_identity (id),
    server_id       bigint      NULL REFERENCES server (id),
    session_id      bigint      NULL,
    payload         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    startup_replay  boolean     NOT NULL DEFAULT false,
    after_data_gap  boolean     NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    processed_at    timestamptz NULL
);
COMMENT ON TABLE  domain_event                IS 'Выводы из наблюдений (join/leave/смена ника), записанные в той же транзакции, что и изменение. Outbox: диспетчер читает необработанные по id и выставляет processed_at.';
COMMENT ON COLUMN domain_event.id             IS 'Внутренний PK, порядок обработки.';
COMMENT ON COLUMN domain_event.event_type     IS 'PLAYER_JOINED_SERVER | PLAYER_LEFT_SERVER | PLAYER_ENTERED_QUEUE | PLAYER_LEFT_QUEUE | PLAYER_NICKNAME_CHANGED.';
COMMENT ON COLUMN domain_event.occurred_at    IS 'Время события по данным наблюдения (для LEFT — first_known_absent_at).';
COMMENT ON COLUMN domain_event.player_id      IS 'Игрок, если применимо.';
COMMENT ON COLUMN domain_event.server_id      IS 'Сервер, если применимо.';
COMMENT ON COLUMN domain_event.session_id     IS 'Сессия (присутствия или очереди), породившая событие. Ключ дедупликации уведомлений.';
COMMENT ON COLUMN domain_event.payload        IS 'Детали: старый/новый ник и т.п.';
COMMENT ON COLUMN domain_event.startup_replay IS 'Событие получено первым poll после старта процесса — не факт входа, а «уже был здесь».';
COMMENT ON COLUMN domain_event.after_data_gap IS 'Событие после разрыва данных — вход мог случиться раньше.';
COMMENT ON COLUMN domain_event.created_at     IS 'Запись строки.';
COMMENT ON COLUMN domain_event.processed_at   IS 'Когда диспетчер обработал. NULL — в очереди.';
CREATE INDEX domain_event_unprocessed_idx ON domain_event (id) WHERE processed_at IS NULL;
CREATE INDEX domain_event_player_idx ON domain_event (player_id, occurred_at DESC);

-- +goose Down
DROP TABLE domain_event;
DROP TABLE player_queue_session;
DROP TABLE player_server_session;
DROP TABLE player_alias;
DROP TABLE player_platform_identity;
DROP TABLE player_identity;
ALTER TABLE server DROP COLUMN tracking_source;
