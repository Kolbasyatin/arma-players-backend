-- +goose Up
-- Свёртки нагрузки и чистка растущих таблиц (AGENTS §12).
--
-- ЗАЧЕМ. Замер 23.09.2026: за 13 дней работы три таблицы дали 3,1 ГБ из 4,1 и растут
-- на ~240 МБ в сутки — poll_run 277 тыс. строк/сутки, domain_event 356 тыс., server_observation 143 тыс.
-- При 10 ГБ диска это месяц до остановки. В отличие от прошлого раза это НЕ распухание:
-- строки живые, и VACUUM тут бессилен. Лечится только тем, чтобы перестать хранить всё подряд.
--
-- Что теряется: поминутная детализация старше срока хранения. Что остаётся: наполненность
-- любого сервера за любую дату — 144 точки в сутки для отслеживаемых, одна для остальных.

CREATE TABLE server_load_10m (
    server_id    bigint      NOT NULL REFERENCES server (id),
    bucket       timestamptz NOT NULL,
    samples      integer     NOT NULL,
    players_avg  numeric(6,2) NULL,
    players_min  integer     NULL,
    players_max  integer     NULL,
    queue_avg    numeric(6,2) NULL,
    queue_max    integer     NULL,
    PRIMARY KEY (server_id, bucket)
);
COMMENT ON TABLE  server_load_10m             IS 'Наполненность отслеживаемых серверов с шагом 10 минут. Считается из server_observation и переживает её чистку: 144 точки в сутки на сервер вместо 720 наблюдений.';
COMMENT ON COLUMN server_load_10m.server_id   IS 'Сервер. Ссылка на исходную запись каталога, как и в server_observation: merge историю не переписывает.';
COMMENT ON COLUMN server_load_10m.bucket      IS 'Начало десятиминутного интервала в UTC.';
COMMENT ON COLUMN server_load_10m.samples     IS 'Сколько наблюдений попало в интервал. Меньше ожидаемого — были неудачные опросы, среднее менее надёжно.';
COMMENT ON COLUMN server_load_10m.players_avg IS 'Средний онлайн за интервал.';
COMMENT ON COLUMN server_load_10m.players_min IS 'Минимальный онлайн за интервал.';
COMMENT ON COLUMN server_load_10m.players_max IS 'Максимальный онлайн за интервал. Именно он нужен авто-правилу отслеживания: рестарт сервера не должен выбрасывать его из выборки.';
COMMENT ON COLUMN server_load_10m.queue_avg   IS 'Средняя длина очереди за интервал.';
COMMENT ON COLUMN server_load_10m.queue_max   IS 'Максимальная длина очереди за интервал.';

CREATE TABLE server_load_daily (
    server_id    bigint       NOT NULL REFERENCES server (id),
    day          date         NOT NULL,
    samples      integer      NOT NULL,
    players_avg  numeric(6,2) NULL,
    players_min  integer      NULL,
    players_max  integer      NULL,
    queue_max    integer      NULL,
    PRIMARY KEY (server_id, day)
);
COMMENT ON TABLE  server_load_daily             IS 'Суточная наполненность ВСЕХ серверов каталога, включая неотслеживаемые: по ним есть только точки суточного скана лобби. Одна строка на сервер в сутки — это ~5000 строк в день на весь каталог.';
COMMENT ON COLUMN server_load_daily.server_id   IS 'Сервер.';
COMMENT ON COLUMN server_load_daily.day         IS 'Сутки в UTC.';
COMMENT ON COLUMN server_load_daily.samples     IS 'Сколько наблюдений за сутки. У неотслеживаемого сервера это число сканов лобби, у отслеживаемого — сотни.';
COMMENT ON COLUMN server_load_daily.players_avg IS 'Средний онлайн за сутки.';
COMMENT ON COLUMN server_load_daily.players_min IS 'Минимальный онлайн за сутки.';
COMMENT ON COLUMN server_load_daily.players_max IS 'Пиковый онлайн за сутки.';
COMMENT ON COLUMN server_load_daily.queue_max   IS 'Максимальная очередь за сутки.';

CREATE TABLE poll_run_daily (
    day       date    NOT NULL,
    poll_type text    NOT NULL,
    status    text    NOT NULL,
    runs      integer NOT NULL,
    PRIMARY KEY (day, poll_type, status)
);
COMMENT ON TABLE  poll_run_daily           IS 'Суточная статистика обращений к Bohemia. Подробный журнал poll_run живёт ограниченный срок, а доля успешных по дням нужна надолго: по ней видно деградацию чужого API.';
COMMENT ON COLUMN poll_run_daily.day       IS 'Сутки в UTC.';
COMMENT ON COLUMN poll_run_daily.poll_type IS 'Тип обращения: LOBBY_SCAN | SEARCH_ROOM | LIST_PLAYERS.';
COMMENT ON COLUMN poll_run_daily.status    IS 'Итог: SUCCESS, ROOM_NOT_FOUND, HTTP_ERROR и прочие из AGENTS §10.';
COMMENT ON COLUMN poll_run_daily.runs      IS 'Сколько обращений с таким итогом за сутки.';

-- Чистка идёт по времени, поэтому индексы по нему обязательны: иначе удаление старого
-- заставит читать таблицу целиком, а она на проде уже миллионы строк.
CREATE INDEX server_observation_observed_at_idx ON server_observation (observed_at);
CREATE INDEX poll_run_finished_at_idx           ON poll_run (finished_at);
CREATE INDEX domain_event_occurred_at_idx       ON domain_event (occurred_at);

-- +goose Down
DROP INDEX IF EXISTS domain_event_occurred_at_idx;
DROP INDEX IF EXISTS poll_run_finished_at_idx;
DROP INDEX IF EXISTS server_observation_observed_at_idx;
DROP TABLE poll_run_daily;
DROP TABLE server_load_daily;
DROP TABLE server_load_10m;
