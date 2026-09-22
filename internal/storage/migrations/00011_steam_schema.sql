-- +goose Up
-- Данные Steam об «избранных» игроках: профиль, друзья, наигранное, баны.
--
-- ОТДЕЛЬНАЯ СХЕМА, а не таблицы в public. Причины две. Это зеркало чужих данных с другим
-- жизненным циклом: его можно целиком выбросить и перекачать, ничего своего не потеряв.
-- И имена не конфликтуют: steam.profile против public.player_identity читается однозначно.
-- В той же БАЗЕ, а не в отдельной — весь смысл в джойнах к player_platform_identity.
CREATE SCHEMA steam;
COMMENT ON SCHEMA steam IS 'Зеркало публичных данных Steam Web API по игрокам из steam.watchlist. Источник истины — steam.snapshot; остальные таблицы производны от неё и восстанавливаются перекачкой.';

-- Кого обогащаем. Данных по всем игрокам не нужно: их десятки тысяч, а интерес точечный.
CREATE TABLE steam.watchlist (
    steam_id    text        PRIMARY KEY,
    added_at    timestamptz NOT NULL DEFAULT now(),
    added_by    text        NOT NULL DEFAULT '',
    note        text        NOT NULL DEFAULT '',
    enabled     boolean     NOT NULL DEFAULT true,
    last_try_at timestamptz NULL,
    last_ok_at  timestamptz NULL,
    last_error  text        NOT NULL DEFAULT ''
);
COMMENT ON TABLE  steam.watchlist             IS 'Список SteamID64, по которым собираем данные Valve. Заполняется вручную или из подписок бота.';
COMMENT ON COLUMN steam.watchlist.steam_id    IS 'SteamID64 (17 цифр) как текст: числом это 64-битное значение, но арифметика над ним бессмысленна.';
COMMENT ON COLUMN steam.watchlist.added_at    IS 'Когда игрок попал в список.';
COMMENT ON COLUMN steam.watchlist.added_by    IS 'Кто добавил: имя оператора или источник (bot, manual).';
COMMENT ON COLUMN steam.watchlist.note        IS 'Зачем добавлен. Свободный текст для человека.';
COMMENT ON COLUMN steam.watchlist.enabled     IS 'false — временно не опрашивать, не удаляя из списка.';
COMMENT ON COLUMN steam.watchlist.last_try_at IS 'Последняя попытка сбора, включая неудачную.';
COMMENT ON COLUMN steam.watchlist.last_ok_at  IS 'Последний успешный сбор.';
COMMENT ON COLUMN steam.watchlist.last_error  IS 'Ошибка последней попытки. Пусто — последняя попытка удалась.';

-- Сырые ответы Valve. ИСТОЧНИК ИСТИНЫ: форма чужая и меняется без предупреждения,
-- поэтому храним как пришло, а колонки извлекаем отдельным шагом.
CREATE TABLE steam.snapshot (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    steam_id   text        NOT NULL,
    source     text        NOT NULL,
    fetched_at timestamptz NOT NULL,
    body       jsonb       NOT NULL,
    body_hash  bytea       NOT NULL
);
COMMENT ON TABLE  steam.snapshot            IS 'Ответы Steam Web API как есть. Новая строка пишется ТОЛЬКО при изменении body_hash: у активного игрока playtime_2weeks меняется каждый день, и без этой проверки таблица росла бы как raw_payload, который занял 5,7 ГБ из 11.';
COMMENT ON COLUMN steam.snapshot.id         IS 'Внутренний PK.';
COMMENT ON COLUMN steam.snapshot.steam_id   IS 'SteamID64 игрока.';
COMMENT ON COLUMN steam.snapshot.source     IS 'Метод Valve: summary (GetPlayerSummaries) | friends (GetFriendList) | games (GetOwnedGames) | bans (GetPlayerBans).';
COMMENT ON COLUMN steam.snapshot.fetched_at IS 'Когда получен ответ.';
COMMENT ON COLUMN steam.snapshot.body       IS 'Тело ответа целиком, без разбора.';
COMMENT ON COLUMN steam.snapshot.body_hash  IS 'SHA-256 канонического представления body. Сравнивается с последним снимком того же источника; совпал — новая строка не пишется.';

-- Дедупликация опирается на «последний снимок этого игрока из этого источника».
CREATE INDEX steam_snapshot_latest_idx ON steam.snapshot (steam_id, source, fetched_at DESC);

-- Текущее состояние: сводка ПО ВСЕМ ЧЕТЫРЁМ источникам в одной строке.
-- Не «профиль из summary»: запрос «кто из наблюдаемых с VAC-баном наиграл больше 500 часов»
-- должен быть одной строкой SQL, а не разбором jsonb.
CREATE TABLE steam.profile (
    steam_id            text        PRIMARY KEY,
    persona_name        text        NOT NULL DEFAULT '',
    real_name           text        NOT NULL DEFAULT '',
    avatar_hash         text        NOT NULL DEFAULT '',
    profile_url         text        NOT NULL DEFAULT '',
    country_code        text        NOT NULL DEFAULT '',
    primary_clan_id     text        NOT NULL DEFAULT '',
    visibility          integer     NULL,
    account_created_at  timestamptz NULL,
    vac_banned          boolean     NULL,
    vac_ban_count       integer     NULL,
    game_ban_count      integer     NULL,
    days_since_last_ban integer     NULL,
    economy_ban         text        NOT NULL DEFAULT '',
    reforger_minutes    integer     NULL,
    reforger_minutes_2w integer     NULL,
    games_visible       boolean     NOT NULL DEFAULT false,
    friends_visible     boolean     NOT NULL DEFAULT false,
    updated_at          timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE  steam.profile                     IS 'Последнее известное состояние игрока в Steam, собранное из всех источников. Производная таблица: теряется без потерь, восстанавливается из steam.snapshot.';
COMMENT ON COLUMN steam.profile.steam_id            IS 'SteamID64. Связь с нашими игроками — через public.player_platform_identity (game_client_type = PLATFORM_PC).';
COMMENT ON COLUMN steam.profile.persona_name        IS 'Отображаемый ник в Steam. С игровым ником не совпадает: в игре человек переименовывается свободно, SteamID64 не меняется никогда.';
COMMENT ON COLUMN steam.profile.real_name           IS 'Поле «настоящее имя», если заполнено. Текст произвольный, доверия не заслуживает.';
COMMENT ON COLUMN steam.profile.avatar_hash         IS 'Хеш аватара; полный URL собирается как https://avatars.steamstatic.com/<hash>_full.jpg.';
COMMENT ON COLUMN steam.profile.profile_url         IS 'Ссылка на профиль. Для аккаунта без кастомного адреса — вид /profiles/<steamid>.';
COMMENT ON COLUMN steam.profile.country_code        IS 'Страна из профиля (loccountrycode), если указана. Заполняется игроком, не проверяется.';
COMMENT ON COLUMN steam.profile.primary_clan_id     IS 'Основная группа Steam. Косвенный признак принадлежности к сообществу.';
COMMENT ON COLUMN steam.profile.visibility          IS 'communityvisibilitystate: 3 — профиль открыт, 1 — закрыт. Открытый профиль НЕ означает открытые игры.';
COMMENT ON COLUMN steam.profile.account_created_at  IS 'Дата создания аккаунта Steam. Сильный признак «свежего» аккаунта.';
COMMENT ON COLUMN steam.profile.vac_banned          IS 'Есть ли VAC-бан. NULL — источник bans ещё не собран.';
COMMENT ON COLUMN steam.profile.vac_ban_count       IS 'Количество VAC-банов.';
COMMENT ON COLUMN steam.profile.game_ban_count      IS 'Количество игровых банов (выданных разработчиком, не Valve).';
COMMENT ON COLUMN steam.profile.days_since_last_ban IS 'Дней с последнего бана. 0 при отсутствии банов — это значение Valve, а не «бан сегодня».';
COMMENT ON COLUMN steam.profile.economy_ban         IS 'Ограничение на обмен: none | probation | banned.';
COMMENT ON COLUMN steam.profile.reforger_minutes    IS 'Всего наиграно в Arma Reforger (appid 1874880), минут. NULL — игры скрыты.';
COMMENT ON COLUMN steam.profile.reforger_minutes_2w IS 'Наиграно в Reforger за две недели, минут. Valve не присылает поле, если игрок не заходил.';
COMMENT ON COLUMN steam.profile.games_visible       IS 'Видны ли игры. Valve на скрытых играх отдаёт HTTP 200 и пустой объект, поэтому отличить «скрыто» от «нет данных» можно только так.';
COMMENT ON COLUMN steam.profile.friends_visible     IS 'Виден ли список друзей.';
COMMENT ON COLUMN steam.profile.updated_at          IS 'Когда состояние последний раз пересчитано из снимков.';

-- Граф друзей. Единственное, ради чего вообще стоит раскладывать JSON в таблицу:
-- вопрос «кто из друзей этого игрока тоже ходит на наши серверы» джойнится к
-- player_platform_identity одним запросом, а в jsonb не задаётся по-человечески.
CREATE TABLE steam.friend_edge (
    steam_id        text        NOT NULL,
    friend_steam_id text        NOT NULL,
    friend_since    timestamptz NULL,
    first_seen_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at    timestamptz NOT NULL DEFAULT now(),
    lost_at         timestamptz NULL,
    PRIMARY KEY (steam_id, friend_steam_id)
);
COMMENT ON TABLE  steam.friend_edge                 IS 'Дружеские связи Steam, направленно от наблюдаемого игрока. Связь двусторонняя по смыслу, но известна нам только со стороны того, чей список мы читали.';
COMMENT ON COLUMN steam.friend_edge.steam_id        IS 'Чей список друзей читали.';
COMMENT ON COLUMN steam.friend_edge.friend_steam_id IS 'Друг. В watchlist он может не значиться — тогда о нём известен только id.';
COMMENT ON COLUMN steam.friend_edge.friend_since    IS 'Когда подружились, по данным Valve. NULL — поле не пришло.';
COMMENT ON COLUMN steam.friend_edge.first_seen_at   IS 'Когда мы впервые увидели эту связь.';
COMMENT ON COLUMN steam.friend_edge.last_seen_at    IS 'Когда видели в последний раз.';
COMMENT ON COLUMN steam.friend_edge.lost_at         IS 'Когда связь пропала из списка (расфрендились). NULL — связь актуальна. Строка не удаляется: исчезновение дружбы — такой же факт, как её появление.';

CREATE INDEX steam_friend_edge_reverse_idx ON steam.friend_edge (friend_steam_id) WHERE lost_at IS NULL;

-- +goose Down
DROP SCHEMA steam CASCADE;
