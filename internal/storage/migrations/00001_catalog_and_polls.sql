-- +goose Up

-- ---------------------------------------------------------------------------
-- raw_payload: сырые ответы Bohemia с ограниченным сроком хранения (AGENTS §12).
-- Создаётся первой: на неё ссылается server_observation.
-- ---------------------------------------------------------------------------
CREATE TABLE raw_payload (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind        text        NOT NULL,
    fetched_at  timestamptz NOT NULL,
    expires_at  timestamptz NOT NULL,
    payload     jsonb       NOT NULL
);
COMMENT ON TABLE  raw_payload            IS 'Сырой JSON ответов Bohemia API. Хранится ограниченное время (retention конфигурируемый) для переигрывания парсера и поиска новых полей.';
COMMENT ON COLUMN raw_payload.id         IS 'Внутренний PK.';
COMMENT ON COLUMN raw_payload.kind       IS 'Тип ответа: SEARCH_ROOMS | LIST_PLAYERS.';
COMMENT ON COLUMN raw_payload.fetched_at IS 'Когда ответ получен (UTC).';
COMMENT ON COLUMN raw_payload.expires_at IS 'После этого момента строку можно удалить фоновой чисткой.';
COMMENT ON COLUMN raw_payload.payload    IS 'Тело ответа как есть.';
CREATE INDEX raw_payload_expires_at_idx ON raw_payload (expires_at);

-- ---------------------------------------------------------------------------
-- server: логический сервер, к которому привязана история (ADR 0004).
-- ---------------------------------------------------------------------------
CREATE TABLE server (
    id                      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    merged_into_server_id   bigint      NULL REFERENCES server (id),
    display_name            text        NOT NULL,
    current_host_address    text        NULL,
    current_room_id         text        NULL,
    tracking_enabled        boolean     NOT NULL DEFAULT false,
    active                  boolean     NOT NULL DEFAULT true,
    first_seen_at           timestamptz NOT NULL,
    last_seen_at            timestamptz NOT NULL,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE  server                       IS 'Логический сервер Arma Reforger. Каноническим считается запись с merged_into_server_id IS NULL; история (наблюдения, сессии) ссылается на исходную запись и никогда не переписывается при merge.';
COMMENT ON COLUMN server.id                    IS 'Внутренний PK. Внешние идентификаторы (адрес, roomId) — атрибуты, см. server_identity_key.';
COMMENT ON COLUMN server.merged_into_server_id IS 'Если не NULL — запись схлопнута в указанный канонический сервер (мягкий merge, обратимый). Цепочки не допускаются.';
COMMENT ON COLUMN server.display_name          IS 'Последнее наблюдённое имя сервера. История имён — в server_observation.';
COMMENT ON COLUMN server.current_host_address  IS 'Последний наблюдённый hostAddress (ip:port игрового порта). По нему tracking ищет актуальную комнату.';
COMMENT ON COLUMN server.current_room_id       IS 'roomId текущей игровой сессии. Не идентичность сервера: может смениться при перерегистрации.';
COMMENT ON COLUMN server.tracking_enabled      IS 'Опрашивать ли listPlayers каждую минуту. Выбор пользователя/правила, не факт из лобби.';
COMMENT ON COLUMN server.active                IS 'Был ли сервер виден при последнем полном скане лобби. false = исчез из каталога, запись сохраняется.';
COMMENT ON COLUMN server.first_seen_at         IS 'Первое наблюдение в лобби.';
COMMENT ON COLUMN server.last_seen_at          IS 'Последнее наблюдение в лобби (любым способом).';
COMMENT ON COLUMN server.created_at            IS 'Создание строки.';
COMMENT ON COLUMN server.updated_at            IS 'Последнее изменение строки.';
CREATE INDEX server_current_host_address_idx ON server (current_host_address);
CREATE INDEX server_tracking_enabled_idx     ON server (tracking_enabled) WHERE tracking_enabled;

-- ---------------------------------------------------------------------------
-- server_identity_key: все наблюдённые внешние ключи сервера с интервалами жизни.
-- ---------------------------------------------------------------------------
CREATE TABLE server_identity_key (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    server_id           bigint      NOT NULL REFERENCES server (id),
    key_type            text        NOT NULL,
    key_value           text        NOT NULL,
    first_seen_at       timestamptz NOT NULL,
    last_seen_at        timestamptz NOT NULL,
    observation_count   integer     NOT NULL DEFAULT 1,
    UNIQUE (server_id, key_type, key_value)
);
COMMENT ON TABLE  server_identity_key                   IS 'Какие внешние ключи (адреса, roomId, sessionId) когда-либо принадлежали серверу. Основа резолюции room → server и подсказок для merge.';
COMMENT ON COLUMN server_identity_key.id                IS 'Внутренний PK.';
COMMENT ON COLUMN server_identity_key.server_id         IS 'Сервер-владелец ключа.';
COMMENT ON COLUMN server_identity_key.key_type          IS 'HOST_ADDRESS | ROOM_ID | SESSION_ID. Список расширяемый.';
COMMENT ON COLUMN server_identity_key.key_value         IS 'Значение ключа как пришло из API.';
COMMENT ON COLUMN server_identity_key.first_seen_at     IS 'Первое наблюдение этого ключа у сервера.';
COMMENT ON COLUMN server_identity_key.last_seen_at      IS 'Последнее наблюдение.';
COMMENT ON COLUMN server_identity_key.observation_count IS 'Сколько раз ключ наблюдался. Для оценки устойчивости.';
CREATE INDEX server_identity_key_lookup_idx ON server_identity_key (key_type, key_value);

-- ---------------------------------------------------------------------------
-- server_observation: снимок состояния сервера в момент наблюдения (observed, не derived).
-- ---------------------------------------------------------------------------
CREATE TABLE server_observation (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    server_id           bigint      NOT NULL REFERENCES server (id),
    observed_at         timestamptz NOT NULL,
    source              text        NOT NULL,
    room_id             text        NULL,
    session_id          text        NULL,
    host_address        text        NULL,
    name                text        NULL,
    scenario_id         text        NULL,
    scenario_name       text        NULL,
    game_version        text        NULL,
    host_type           text        NULL,
    platform_name       text        NULL,
    ping_site_id        text        NULL,
    player_count        integer     NULL,
    player_limit        integer     NULL,
    queue_size          integer     NULL,
    queue_max_size      integer     NULL,
    queue_avg_wait_time integer     NULL,
    direct_join_code    text        NULL,
    battl_eye           boolean     NULL,
    official            boolean     NULL,
    joinable            boolean     NULL,
    visible             boolean     NULL,
    password_protected  boolean     NULL,
    runtime_fps         integer     NULL,
    runtime_memory      bigint      NULL,
    data_updated_at     timestamptz NULL,
    details_updated_at  timestamptz NULL,
    raw_payload_id      bigint      NULL REFERENCES raw_payload (id) ON DELETE SET NULL
);
COMMENT ON TABLE  server_observation                     IS 'Что Bohemia сообщила о сервере в момент observed_at. Факты наблюдения; выводы (события, изменения) строятся отдельно.';
COMMENT ON COLUMN server_observation.id                  IS 'Внутренний PK.';
COMMENT ON COLUMN server_observation.server_id           IS 'Сервер, к которому отнесено наблюдение (исходная запись, не канонический).';
COMMENT ON COLUMN server_observation.observed_at         IS 'Момент получения ответа (наши часы, UTC).';
COMMENT ON COLUMN server_observation.source              IS 'Откуда снимок: LOBBY_SCAN (суточный полный скан) | TRACKING_POLL (минутный опрос).';
COMMENT ON COLUMN server_observation.room_id             IS 'roomId на момент наблюдения.';
COMMENT ON COLUMN server_observation.session_id          IS 'sessionId из ответа. Стабильность исследуется.';
COMMENT ON COLUMN server_observation.host_address        IS 'hostAddress на момент наблюдения.';
COMMENT ON COLUMN server_observation.name                IS 'Имя сервера.';
COMMENT ON COLUMN server_observation.scenario_id         IS 'Идентификатор сценария, например {ECC61978EDCC2B5A}Missions/23_Campaign.conf.';
COMMENT ON COLUMN server_observation.scenario_name       IS 'Локализационный ключ сценария, например #AR-Campaign_ScenarioName_Everon.';
COMMENT ON COLUMN server_observation.game_version        IS 'Версия игры на сервере.';
COMMENT ON COLUMN server_observation.host_type           IS 'Тип хоста, наблюдалось CommunityDs.';
COMMENT ON COLUMN server_observation.platform_name       IS 'ОС сервера, наблюдалось Windows.';
COMMENT ON COLUMN server_observation.ping_site_id        IS 'Регион ping-сайта, например frankfurt.';
COMMENT ON COLUMN server_observation.player_count        IS 'Игроков внутри по данным лобби.';
COMMENT ON COLUMN server_observation.player_limit        IS 'Лимит игроков.';
COMMENT ON COLUMN server_observation.queue_size          IS 'Длина очереди на вход.';
COMMENT ON COLUMN server_observation.queue_max_size      IS 'Максимум очереди.';
COMMENT ON COLUMN server_observation.queue_avg_wait_time IS 'Среднее ожидание в очереди, секунды.';
COMMENT ON COLUMN server_observation.direct_join_code    IS 'Direct Join Code. Нестабилен: менялся без переезда сервера.';
COMMENT ON COLUMN server_observation.battl_eye           IS 'Включён ли BattlEye.';
COMMENT ON COLUMN server_observation.official            IS 'Официальный сервер Bohemia.';
COMMENT ON COLUMN server_observation.joinable            IS 'Можно ли подключиться.';
COMMENT ON COLUMN server_observation.visible             IS 'Виден ли в браузере серверов.';
COMMENT ON COLUMN server_observation.password_protected  IS 'Требуется ли пароль.';
COMMENT ON COLUMN server_observation.runtime_fps         IS 'FPS сервера из runtimeStats.';
COMMENT ON COLUMN server_observation.runtime_memory      IS 'Память сервера из runtimeStats (единицы Bohemia, предположительно KiB).';
COMMENT ON COLUMN server_observation.data_updated_at     IS 'Поле updated из ответа: последний heartbeat сервера в каталог. Показывает свежесть данных лобби.';
COMMENT ON COLUMN server_observation.details_updated_at  IS 'Поле detailsUpdatedAt из ответа: последнее изменение описания комнаты.';
COMMENT ON COLUMN server_observation.raw_payload_id      IS 'Сырой ответ, из которого получен снимок. NULL после чистки retention.';
CREATE INDEX server_observation_server_time_idx ON server_observation (server_id, observed_at DESC);

-- ---------------------------------------------------------------------------
-- poll_run: журнал каждого обращения к Bohemia. poll failed != zero players (AGENTS §10).
-- ---------------------------------------------------------------------------
CREATE TABLE poll_run (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    server_id       bigint      NULL REFERENCES server (id),
    poll_type       text        NOT NULL,
    started_at      timestamptz NOT NULL,
    finished_at     timestamptz NOT NULL,
    status          text        NOT NULL,
    room_id         text        NULL,
    http_status     integer     NULL,
    error_message   text        NULL,
    connected_count integer     NULL,
    queue_count     integer     NULL,
    data_updated_at timestamptz NULL
);
COMMENT ON TABLE  poll_run                 IS 'Каждый запрос к Bohemia: успех или вид ошибки. Неуспешный poll не меняет состояние игроков и не считается пустым сервером.';
COMMENT ON COLUMN poll_run.id              IS 'Внутренний PK.';
COMMENT ON COLUMN poll_run.server_id       IS 'Сервер, если запрос относился к конкретному серверу. NULL для полного скана лобби.';
COMMENT ON COLUMN poll_run.poll_type       IS 'LOBBY_SCAN | RESOLVE_ROOM | LIST_PLAYERS.';
COMMENT ON COLUMN poll_run.started_at      IS 'Начало запроса.';
COMMENT ON COLUMN poll_run.finished_at     IS 'Конец запроса (успех или ошибка).';
COMMENT ON COLUMN poll_run.status          IS 'SUCCESS или вид ошибки: DNS_FAILED, CONNECTION_TIMEOUT, CONNECTION_REFUSED, TLS_ERROR, AUTH_ERROR, HTTP_ERROR, INVALID_JSON, ROOM_NOT_FOUND, TOKEN_UNAVAILABLE, INTERNAL_ERROR.';
COMMENT ON COLUMN poll_run.room_id         IS 'roomId, с которым делался запрос, если применимо.';
COMMENT ON COLUMN poll_run.http_status     IS 'HTTP-статус ответа, если ответ получен.';
COMMENT ON COLUMN poll_run.error_message   IS 'Текст ошибки для диагностики. Решения принимаются по status, не по тексту.';
COMMENT ON COLUMN poll_run.connected_count IS 'Игроков в connectedPlayers при успешном listPlayers.';
COMMENT ON COLUMN poll_run.queue_count     IS 'Игроков в queuePlayers при успешном listPlayers.';
COMMENT ON COLUMN poll_run.data_updated_at IS 'Свежесть данных лобби (updated комнаты) на момент запроса.';
CREATE INDEX poll_run_server_time_idx ON poll_run (server_id, started_at DESC);
CREATE INDEX poll_run_status_time_idx ON poll_run (status, started_at DESC);

-- +goose Down
DROP TABLE poll_run;
DROP TABLE server_observation;
DROP TABLE server_identity_key;
DROP TABLE server;
DROP TABLE raw_payload;
