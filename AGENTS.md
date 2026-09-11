# AGENTS.md — Arma Reforger Player Observer

> **Статус: MVP-коллектор работает в проде с 2026-09-10.** Этот файл — постановка задачи и правила.
> Принятые решения — в [`docs/adr`](docs/adr/README.md), проверенные факты о Bohemia API —
> в [`docs/research/bohemia-api-observations.md`](docs/research/bohemia-api-observations.md),
> контракт REST API — [`docs/api.md`](docs/api.md), открытые задачи — [`docs/NEXT.md`](docs/NEXT.md).
> Где текст ниже расходится с research/ADR — верны research/ADR: они основаны на живых данных.
>
> **Что уже сделано:** каталог серверов (полный скан лобби), выбор серверов для опроса (ручной список + авто
> по пиковому онлайну, ADR 0008), опрос игроков с распределением запросов по интервалу, идентичности/алиасы/
> платформенные id, presence- и queue-сессии с интервалом неопределённости, доменные события (outbox),
> журнал `poll_run`, retention сырья, служебный HTTP и REST API для внешнего бота, деплой (GHCR + compose + systemd).
>
> **Чего ещё нет:** свёртки нагрузки (10-минутные, решено делать), социальная аналитика, графы, ручной merge
> серверов через API, web-интерфейс, события уровня сервера (`NAME_CHANGED` и др.).

## 1. Назначение проекта

Этот проект — отдельная система наблюдения за публичными серверами и игроками **Arma Reforger**.

Проект полностью независим от `ts-monitoring` / `teamspeakbot`.

Нельзя:
- подключаться к БД `ts-monitoring`;
- переиспользовать его таблицы как источник истины;
- делать новый сервис зависимым от структуры `monitored_servers`;
- добавлять новую логику player tracking внутрь `ts-monitoring`.

Можно:
- использовать отдельные идеи/находки, полученные при исследовании Bohemia API;
- использовать уже существующий отдельный сервис получения BI access token, если он доступен;
- при необходимости в будущем вынести общий каталог серверов в отдельный сервис, но **не делать это заранее**.

Главная идея проекта:

> регулярно наблюдать за выбранными серверами Arma Reforger, фиксировать присутствие игроков и изменение их идентичностей/ников, строить историю посещений, социальные связи и уведомлять о появлении выбранных игроков.

Проект состоит минимум из двух независимых приложений:

```text
reforger-observer-backend
reforger-observer-web
```

На старте backend остаётся **одним приложением с внутренними модулями**, а не набором микросервисов.

Не использовать без необходимости:
- Kafka;
- RabbitMQ;
- Redis Streams;
- отдельные сервисы на каждый bounded context;
- распределённую архитектуру ради архитектуры.

Сначала нужен простой, надёжный сборщик данных.

---

# 2. Основные направления продукта

## 2.1. Каталог серверов

Раз в сутки выполняется полный скан публичного lobby Arma Reforger (интервал конфигурируемый;
на проде сейчас 2 часа). В лобби ~5050 серверов, скан — 11 страниц по 500 комнат, ~12 секунд.

Цель:
- получить список существующих серверов;
- добавлять новые серверы в локальный каталог;
- обновлять уже известные;
- отслеживать изменения свойств серверов;
- отмечать исчезновение/возвращение серверов;
- позволять пользователю выбрать, какие серверы нужно опрашивать часто и собирать по ним игроков.

Каталог серверов принадлежит **этому проекту**.

Не читать список серверов из `ts-monitoring`.

Серверы могут иметь историю изменений:
- название;
- адрес;
- сценарий;
- лимит игроков;
- моды;
- Direct Join Code;
- BattlEye;
- host/platform;
- другие интересные поля ответа Bohemia.

**Идентичность сервера — проверено на данных 2026-09-11 (ADR 0004):**
- за сутки **129 серверов сменили порт или IP, сохранив `roomId`** (хостинги перераспределяют порты и переезжают между IP);
- **18 серверов сменили `roomId`** при рестарте, оставшись на своём адресе;
- `sessionId` не уникален (один и тот же у десятков комнат) — как identity не годится;
- `directJoinCode` меняется без переезда — как identity не годится.

Отсюда правило резолюции: **сначала `ROOM_ID`, затем `HOST_ADDRESS`**, и ни один ключ не считается вечным.
Все наблюдённые ключи хранятся с интервалами жизни в `server_identity_key`. Дубли, возникшие при переезде адреса,
схлопываются мягким merge (`server_merge`, обратимо), история не переписывается.

---

## 2.2. Tracking игроков

Для серверов, включённых в tracking, делается частый polling.

Начальная целевая частота:

```text
1 раз в минуту
```

Это не жёсткая константа архитектуры. Интервал конфигурируемый (`POLL_INTERVAL`; на проде сейчас 2 минуты).

**Запросы распределяются по интервалу**, а не уходят залпом: N серверов → старт одного опроса каждые
`INTERVAL/N`. Нагрузка на Bohemia ровная, `POLL_CONCURRENCY` ограничивает одновременные опросы на случай
медленных ответов. Один опрос сервера = 2 запроса (`rooms/search` по адресу + `listPlayers`).

Какие серверы попадают в tracking — ADR 0008: ручной список адресов (`TRACK_HOST_ADDRESSES`, без лимита)
плюс авто-правило по **пиковому** онлайну за окно (`TRACK_AUTO_MIN_PLAYERS`, `TRACK_AUTO_MAX_SERVERS`,
`TRACK_AUTO_LOOKBACK`). Пик, а не последний снимок: иначе рестарт сервера в момент скана выбрасывал бы его из выборки.

Каждый успешный poll должен:
1. найти актуальную room по `hostAddress`; если комнаты по адресу нет — попробовать сохранённый `current_room_id`
   (при переезде адреса он жив, и сессии игроков не рвутся);
2. получить `roomId`;
3. вызвать `listPlayers`;
4. разобрать:
   - `connectedPlayers`;
   - `queuePlayers`;
5. сравнить состояние с предыдущим успешным наблюдением;
6. обновить identities, aliases и presence sessions;
7. породить доменные события только там, где это действительно можно заключить из данных.

---

## 2.3. Социальные паттерны

После накопления истории строится аналитический слой.

Нас интересуют:
- сколько времени два игрока проводили одновременно на одном сервере;
- как часто они входили примерно одновременно;
- как часто уходили примерно одновременно;
- как часто переходили между серверами похожим маршрутом;
- насколько часто повторяются такие совместные сессии;
- устойчивые группы игроков;
- игроки с высокой взаимной связностью.

Это **association analysis**, а не доказательство дружбы или координации.

Не использовать формулировки вроде:

```text
friend_probability
cheater_probability
```

Предпочтительные нейтральные термины:

```text
association_score
co_presence_score
shared_session_count
shared_minutes
same_join_window_count
same_leave_window_count
```

Если позже появятся данные о faction/team (например через мод), можно расширить аналитику.

Пока без faction нельзя делать утверждение:

> эти игроки заходят за разные стороны и сливают информацию.

Можно только выделять аномальные паттерны для дальнейшего анализа.

---

## 2.4. Watchlist / подписки на игроков

Пользователь frontend должен иметь возможность выбрать игрока и подписаться на его появление.

Пример поведения:

> уведомить меня, когда Player X появится на любом отслеживаемом сервере.

Дополнительно:
- только на конкретном сервере;
- в будущем — на группе серверов;
- в будущем — при появлении любого игрока из группы.

Backend должен породить доменное событие:

```text
PLAYER_JOINED_SERVER
```

с данными:
- player;
- server;
- detected_at;
- confidence / observation metadata при необходимости.

**Решено 2026-09-11: подписки и доставка — вне этого сервиса.** Телеграм-бот уже существует отдельным проектом
(`teamspeakbot`), второго бота не делаем. Observer остаётся источником данных и отдаёт ленту событий по REST
(`docs/api.md`): потребитель держит **свой курсор** (`after` = последний обработанный `id`) и сам решает,
кому и что писать. Ни таблиц подписок, ни транспортов, ни Watchlist Matcher внутри observer нет.

```text
Collector
   ↓
Domain Event (outbox, таблица domain_event)
   ↓
GET /events?after=<курсор>&player_ids=…   ← бот тянет сам, каждые 10–20 с
   ↓
подписки, чаты, тексты сообщений — на стороне бота
```

Почему pull, а не push в бот: не нужен endpoint у бота, нет ретраев и потери порядка, при перезапуске бота
ничего не теряется. Цена — задержка до 20 секунд поверх интервала опроса.

События помечены флагами, чтобы бот не рассылал ложные уведомления:
- `startup_replay` — первый опрос сервера после старта observer: игрок «уже был здесь», это не вход. В ленту по умолчанию не попадает;
- `after_data_gap` — вход замечен после разрыва данных: момент входа неточен.

---

## 2.5. Надёжность наблюдения

Это отдельное важное направление проекта.

Система наблюдения сама может:
- потерять сеть;
- не разрешить DNS;
- получить timeout;
- получить TLS error;
- получить 401/403;
- получить 500 от Bohemia;
- получить некорректный JSON;
- не найти room;
- потерять BI token;
- перезапуститься;
- не работать несколько минут или часов.

Нельзя путать:

```text
игрок вышел
```

и:

```text
мы перестали получать данные
```

Это фундаментальное правило проекта.

---

# 3. Уже подтверждённые Bohemia API-находки

Ниже — undocumented/internal backend API, снятый с реального клиента и проверенный вручную.

Он может меняться между версиями игры.

Протокольные значения должны быть конфигурируемыми:
- User-Agent;
- clientVersion;
- platformId;
- gameClientType;
- base URL.

Текущие исследованные значения:

```text
platformId = ReforgerSteam
gameClientType = PLATFORM_PC
clientVersion = 1.8.0
User-Agent = Arma Reforger/1.8.0.13 (Client; Windows)
```

## 3.1. Search rooms

```http
POST https://api-ar-game.bistudio.com/game-api/api/v1.0/lobby/rooms/search
```

Используется для:
- полного списка серверов;
- поиска по text;
- поиска по hostAddress;
- получения room metadata.

Ответ room содержит, среди прочего:
- id;
- scenarioId;
- name;
- scenarioName;
- gameVersion;
- hostType;
- official;
- joinable;
- visible;
- passwordProtected;
- hostAddress;
- playerCountLimit;
- playerCount;
- directJoinCode;
- supportedGameClientTypes;
- mods;
- battlEye;
- favorite;
- pingSiteId;
- platformName;
- runtimeStats;
- sessionId;
- joinQueue и другие поля.

В `joinQueue` уже наблюдались/использовались поля:

```text
type (наблюдалось REGULAR)
size
maxSize
positionAvgWaitTime
```

**Полный набор ключей комнаты (снято 2026-09-11, 28 полей).** Кроме перечисленного выше есть ещё
`detailsUpdatedAt`, `favorite`, `flags` (наблюдалось 1), `hostedScenarioModId`, `lastJoinedAt`.
`mods` — массив `{modId, name, version}`, до 179 модов на комнату; они и определяют размер ответа (~7 КБ на комнату).

**`lightweight: true` ничем не отличается от `false`** — тот же набор полей, экономии нет.
`limit` принимается до **500**; `from` работает как ожидалось (`searchFrom` в ответе совпадает).
`totalCount` плавает между страницами: серверы приходят и уходят во время скана, поэтому на него не полагаться,
а обходить страницы до короткой или пустой.

---

## 3.2. List players

```http
POST https://api-ar-game.bistudio.com/game-api/api/v1.0/lobby/rooms/listPlayers
```

Минимально подтверждённое тело запроса:

```json
{
  "roomId": "...",
  "accessToken": "...",
  "platformId": "ReforgerSteam",
  "clientVersion": "1.8.0"
}
```

Ответ:

```json
{
  "connectedPlayers": [...],
  "queuePlayers": [...]
}
```

Объект игрока подтверждённого формата:

```json
{
  "userId": "4537e0d4-f960-46ac-bafc-a0ad390b41ea",
  "username": "Salat Majompski",
  "gameClientType": "PLATFORM_PC",
  "platformUserId": "76561198884181842"
}
```

Для `PLATFORM_PC` `platformUserId` выглядит как SteamID64.

`queuePlayers` содержит такие же объекты игроков.

На данный момент `listPlayers` **не отдаёт явно**:
- позицию в очереди;
- joinedAt;
- queueStartedAt;
- faction;
- squad;
- player score;
- kill/death stats.

**Порядок `queuePlayers` ≠ очередь — опровергнуто 2026-09-11.** Проследили двух игроков через 26 и 16
последовательных опросов: индекс держится на постоянном расстоянии от конца массива, новые входящие
появляются на произвольных позициях, ушедшие исчезают с произвольных. Порядок стабилен (похож на номер слота),
но с очерёдностью не связан. **Позицию игрока в очереди из lobby API получить нельзя**, событий «стоит под номером N»
по этому источнику не будет. Остаются `PLAYER_ENTERED_QUEUE` / `PLAYER_LEFT_QUEUE` с длительностью ожидания
и результатом (`JOINED_SERVER` / `LEFT_QUEUE` / `UNKNOWN`).

**Коды ошибок `listPlayers` (проверено на живом API):**

| Ситуация | Ответ |
|---|---|
| несуществующий или устаревший `roomId` | `404`, `apiCode: MpRoomNotFound` — **не** пустой список, поэтому рестарт нельзя спутать с массовым выходом |
| комната с паролем | `400`, `apiCode: InvalidInput`, «Room is password protected but password or inviteToken wasn't provided» |

Запароленные комнаты не отдают список игроков чужому аккаунту — их надо исключать из tracking.
В игровом клиенте есть фильтр «только без пароля»; имя поля в теле `rooms/search` нужно снять с клиента через прокси
(не угадывать), затем добавить в запрос.

---

## 3.3. Другие найденные client-side endpoints

Исторически/реверсом найдены:

```text
POST /game-api/api/v1.0/lobby/rooms/getRoomsByIds
POST /game-api/api/v1.0/lobby/rooms/join
POST /game-api/api/v1.0/lobby/rooms/verifyPassword
POST /game-api/api/v1.0/lobby/getPingSites
POST /game-api/api/v1.0/blockList/listBlocked
POST /game-api/api/v1.0/session/login
POST /game-api/api/v1.0/sendTdEvents
GET  /game-api/api/v1.0/world
```

Часть endpoint могла измениться.

Каждый новый endpoint нужно проверять на живом backend.

---

## 3.4. Dedicated Server S2S endpoints

Исторически найдены:

```text
POST /game-api/s2s-api/v1.0/lobby/dedicatedServers/registerUnmanagedServer
POST /game-api/s2s-api/v1.0/lobby/dedicatedServers/heartBeat
POST /game-api/s2s-api/v1.0/lobby/rooms/register
POST /game-api/s2s-api/v1.0/lobby/rooms/acceptPlayer
POST /game-api/s2s-api/v1.0/lobby/rooms/removePlayer
POST /game-api/s2s-api/v1.0/lobby/rooms/listActiveBans
POST /game-api/s2s-api/v1.0/lobby/rooms/createBan
POST /game-api/s2s-api/v1.0/lobby/rooms/removeBans
POST /game-api/s2s-api/v1.0/sendTdEvents
```

Это S2S API.

Не пытаться использовать обычный пользовательский BI token вместо S2S credentials.

Особый интерес:
- `heartBeat` — вероятный источник актуального списка игроков/состояния server room в backend;
- `acceptPlayer` — исторически содержал более богатые сведения об identity/platform.

Изменение queue priority / whitelist / VIP проходки должно исследоваться **отдельно**.
Не смешивать это с текущим observer.
Возможно это server-side/RCON/S2S область.

---

# 4. Получение BI access token

Уже существует отдельный сервис `arma-reforger-hz`, который умеет:
- иметь Steam refresh token;
- получать Steam game ticket;
- обменивать его через Bohemia Identity;
- получать BI access token.

У него есть endpoint:

```http
GET /token
```

Пример Docker-сетевого адреса:

```text
http://arma-reforger-hz:8080/token
```

Ответ содержит:

```json
{
  "accessToken": "...",
  "expiresAt": "..."
}
```

Новый observer может использовать этот сервис как **отдельную инфраструктурную зависимость**.

Не дублировать сложную Steam auth-логику в observer, пока нет причины.

Observer должен уметь:
- получить token;
- кэшировать его разумно;
- обновить при expiry;
- при 401/403 инвалидировать кэш;
- отличать auth failure от network failure.

---

# 5. Идентификация игрока

## 5.1. Главный внешний identity

Основным внешним идентификатором игрока является:

```text
Bohemia userId
```

из `listPlayers`.

Пример:

```text
4537e0d4-f960-46ac-bafc-a0ad390b41ea
```

В доменной модели лучше называть его:

```text
bohemia_user_id
```

или

```text
bi_identity_id
```

Потому что это не просто nickname и не Steam account.

---

## 5.2. Platform identity

Дополнительно сохранять:

```text
gameClientType
platformUserId
```

Для PC:

```text
gameClientType = PLATFORM_PC
platformUserId ≈ SteamID64
```

**Наблюдённые платформы (первые сутки, 40 000 игроков):** `PLATFORM_PC` 58% (SteamID64),
`PLATFORM_PSN` 26% (19-значное число), `PLATFORM_XBL` 15% (40 hex-символов).
Формат `platformUserId` зависит от платформы — хранить текстом и всегда в паре с `gameClientType`.
Ни одного `platformUserId`, общего для двух разных `bohemia_user_id`, пока не встречено.

Нельзя использовать nickname как identity.

`username` — изменяемый alias.

---

## 5.3. Не делать преждевременных UNIQUE предположений

Не ставить сразу жёсткое ограничение:

```text
platformUserId UNIQUE
```

как единственную истину о человеке.

Нужно наблюдать реальные данные.

Возможные аномалии, которые система должна сохранить, а не уничтожить:

```text
Bohemia userId A → SteamID X
Bohemia userId B → SteamID X
```

или:

```text
Bohemia userId A → SteamID X
Bohemia userId A → SteamID Y
```

Если спустя длительное время будет доказана строгая связь 1:1 — constraint можно усилить.

---

# 6. Базовая модель данных

Названия таблиц можно менять, но смысл должен сохраняться.

> **Реализованная схема (миграции `internal/storage/migrations`, 14 таблиц).** Ниже — постановка; фактические
> имена и колонки с комментариями `COMMENT ON` смотреть в миграциях или `\d+` в psql.
>
> | Таблица | Назначение |
> |---|---|
> | `server` | логический сервер, `merged_into_server_id` = псевдоним канонического |
> | `server_identity_key` | наблюдённые `HOST_ADDRESS` / `ROOM_ID` / `SESSION_ID` с интервалами жизни |
> | `server_merge` | журнал схлопываний, обратимо |
> | `server_observation` | снимок состояния сервера (`source`: `LOBBY_SCAN` / `TRACKING_POLL`) |
> | `mod`, `server_mod` | справочник модов и история их версий на сервере (обновление по хешу набора) |
> | `player_identity` | игрок = `bohemia_user_id` (единственный UNIQUE) |
> | `player_platform_identity` | Steam / PSN / XBL id с историей |
> | `player_alias` | история ников |
> | `player_server_session` | визит на сервер, с `nickname` визита и интервалом неопределённости |
> | `player_queue_session` | ожидание в очереди, с результатом |
> | `domain_event` | derived-события, outbox для внешних потребителей |
> | `poll_run` | журнал всех запросов к Bohemia со статусом |
> | `raw_payload` | сырые ответы с `expires_at`, чистятся фоном |
>
> Таблицы `server_event` (§6.3) и `server_match_suggestion` (ADR 0004) ещё не созданы.
> **Требование проекта: у каждой таблицы и каждой колонки есть `COMMENT ON`** — проверяется интеграционным тестом.

## 6.1. servers

```text
servers
-------
id                  internal PK
current_room_id     nullable
host_address
first_seen_at
last_seen_at
tracking_enabled
active
created_at
updated_at
```

Не считать `current_room_id` вечным identity. Фактически в `server` есть также `display_name`,
`tracking_source` (`MANUAL` / `AUTO`, откуда взялся флаг), `mod_set_hash` и `merged_into_server_id`.

---

## 6.2. server_observation / server_state

Хранит наблюдённое состояние сервера.

Пример:

```text
server_observation
------------------
id
server_id
observed_at
room_id
name
scenario_id
scenario_name
game_version
player_count
player_limit
direct_join_code
platform_name
battl_eye
queue_size
queue_max_size
queue_avg_wait_time
raw_payload / selected_json
```

На MVP можно хранить нормализованные поля + raw JSON некоторое ограниченное время.

**Реализовано полнее:** в снимке лежат все поля ответа, включая `session_id`, `host_type`, `ping_site_id`,
`official`/`joinable`/`visible`/`password_protected`, `runtime_fps`/`runtime_memory`, `queue_type`, `flags`,
`supported_game_client_types`, `mod_count`/`mod_set_hash`, `hosted_scenario_mod_id`, `data_updated_at`,
`details_updated_at`, `last_joined_at`. Моды — в `server_mod`. То есть **всё содержимое ответа разложено
по таблицам, и отключение raw ничего не теряет** (проверено 2026-09-11).

---

## 6.3. server_event

История важных изменений:

```text
server_event
------------
id
server_id
event_type
occurred_at
old_value
new_value
metadata
```

Типы:

```text
SERVER_DISCOVERED
SERVER_DISAPPEARED
SERVER_REAPPEARED
NAME_CHANGED
ADDRESS_CHANGED
SCENARIO_CHANGED
PLAYER_LIMIT_CHANGED
MOD_SET_CHANGED
```

Не создавать event, если изменение не подтверждено достаточным наблюдением.

---

## 6.4. player_identity

```text
player_identity
---------------
id                      internal PK
bohemia_user_id
first_seen_at
last_seen_at
created_at
updated_at
```

---

## 6.5. player_platform_identity

Рекомендуется отделить platform mapping от основной identity:

```text
player_platform_identity
------------------------
id
player_id
game_client_type
platform_user_id
first_seen_at
last_seen_at
```

Это позволяет исторически наблюдать несколько platform mappings.

---

## 6.6. player_alias

```text
player_alias
------------
id
player_id
nickname
first_seen_at
last_seen_at
observation_count
```

Nickname:
- не identity;
- может меняться;
- может использоваться несколькими игроками;
- должен сохраняться как история.

Нужно уметь отвечать:
- какие ники использовал конкретный player;
- какие players использовали конкретный nickname.

---

# 7. Presence sessions

Не хранить только "current online".

Нужна история сессий.

## 7.1. player_server_session

```text
player_server_session
---------------------
id
player_id
server_id
first_seen_at
last_seen_at
first_known_absent_at nullable
ended_at nullable
status
created_at
updated_at
```

Поскольку polling дискретный, истинное время выхода неизвестно точно.

Например:

```text
12:01 player seen
12:02 poll failed
12:03 poll failed
12:04 player absent on successful poll
```

Корректное знание:

```text
last_seen_at = 12:01
first_known_absent_at = 12:04
```

Нельзя утверждать, что игрок вышел в 12:02.

---

## 7.2. Grace / подтверждение выхода

Рекомендуемый стартовый алгоритм:

```text
successful poll + player present
    -> online

successful poll + player absent once
    -> suspected_gone

successful poll + player absent second time
    -> closed session

failed poll
    -> состояние игрока не меняется
```

Количество подтверждений должно быть конфигурируемым.

Это защищает от:
- разовых рассинхронизаций backend;
- кратких дыр;
- частично обновлённых данных.

---

# 8. Queue sessions

Очередь хранится отдельно от нахождения внутри сервера.

```text
player_queue_session
--------------------
id
player_id
server_id
first_seen_at
last_seen_at
first_known_absent_at nullable
ended_at nullable
result nullable
```

Возможный `result`:

```text
JOINED_SERVER
LEFT_QUEUE
UNKNOWN
```

`JOINED_SERVER` можно вывести, если:
- player был в queue;
- затем исчез из queue;
- появился в connectedPlayers в разумном временном окне.

Не делать вывод, если между наблюдениями была большая data gap.

---

# 9. Observation model

Нужно разделять:

```text
Observation
```

и:

```text
Derived Event
```

Bohemia сообщает факты наблюдения.

Например:

```text
12:01 connected = [A, B, C]
12:02 connected = [A, B, D]
```

Наш diff выводит:

```text
C probably left
D joined
```

Архитектура:

```text
Bohemia API
   ↓
Raw/Normalized Observation
   ↓
Diff Engine
   ↓
Domain Events
   ↓
Sessions / Watchlists / Analytics
```

---

# 10. Poll run / качество данных

Каждый запрос/цикл должен быть наблюдаемым.

Рекомендуемая таблица:

```text
poll_run
--------
id
server_id nullable
poll_type
started_at
finished_at
status
room_id nullable
http_status nullable
error_type nullable
error_message nullable
connected_count nullable
queue_count nullable
data_updated_at nullable
```

Возможные статусы/ошибки:

```text
SUCCESS
DNS_FAILED
CONNECTION_TIMEOUT
CONNECTION_REFUSED
TLS_ERROR
AUTH_ERROR
HTTP_ERROR
BOHEMIA_ERROR
INVALID_JSON
INVALID_RESPONSE
ROOM_NOT_FOUND
TOKEN_UNAVAILABLE
INTERNAL_ERROR
```

Наблюдённые на практике статусы за первые сутки: `SUCCESS` 97%, `ROOM_NOT_FOUND` (сервер переехал или
перезапускается), `HTTP_ERROR` (почти всегда — комната с паролем, §3.2). `AUTH_ERROR`, таймаутов и `429`
за ~85 000 запросов не наблюдалось.

Очень важно:

```text
poll failed != zero players
```

Если poll неуспешный:
- не закрывать сессии игроков;
- не отправлять PLAYER_LEFT_SERVER;
- не считать сервер пустым;
- frontend должен показывать устаревание данных.

---

# 11. Data freshness

Для любого текущего состояния нужно знать:

```text
observed_at
data_updated_at
```

Если Bohemia room содержит timestamp heartbeat (`updated`), сохранять его отдельно.

Frontend должен уметь показывать:

```text
Данные не обновлялись 8 минут
```

а не ложный:

```text
0 игроков
```

---

# 12. Raw payload retention

Поскольку API undocumented и ещё исследуется, полезно временно сохранять raw payload.

Начальный вариант:
- selected normalized fields хранятся постоянно;
- raw JSON хранится 7–30 дней;
- retention конфигурируемый.

Это позволит позже:
- найти новые поля;
- переиграть parser;
- проверить гипотезы;
- восстановить ошибки старого parsing.

Не обязательно сохранять каждый полный payload навечно.

### Бюджет диска и свёртки (решено 2026-09-11)

На прод-сервере под всё ~10 ГБ. Замеры первых суток при 107 серверах и опросе раз в 2 минуты:
raw payload ~450 МБ за 15 часов (95% объёма БД), остальные таблицы ~140 МБ.

Поскольку **всё содержимое ответов теперь разложено по таблицам** (§6), raw нужен только для отладки формата:
`RAW_RETENTION` можно держать в пределах 48 часов, ничего не теряя. Статистика и графики от raw не зависят.

План хранения (реализовать после недели наблюдений, см. `docs/NEXT.md`):
1. raw — только от суточного скана, ответы минутного опроса не сохранять;
2. `server_observation` из tracking — при изменении описательных полей + контрольный снимок;
3. `poll_run` — 7 дней подробно, дальше суточная статистика;
4. **свёртки нагрузки с гранулярностью 10 минут** (`server_load_10m`: avg/min/max онлайн, avg/max очередь,
   входы/выходы, число замеров) для отслеживаемых серверов и суточные (`server_load_daily`) для всех ~5050.
   Цель: график наполненности любого сервера за любую дату год назад — 144 точки в день для отслеживаемых,
   1 точка в день для остальных. Поминутная детализация живёт 7 дней.

Свёртки идемпотентны (`ON CONFLICT DO UPDATE`), считаются фоновой задачей раз в час; поминутные строки
удаляются только после того, как их интервал свёрнут.

---

# 13. Discovery серверов

Раз в сутки:

```text
full lobby scan
```

Нужно:
1. пройти pagination;
2. получить все rooms;
3. upsert локальные servers;
4. обновить first_seen / last_seen;
5. создать server events при изменениях;
6. пометить давно отсутствующие rooms;
7. не удалять исторические серверы физически.

Параметр "tracking_enabled" выбирается отдельно пользователем/администратором.

То есть:

```text
server exists in catalog
```

не означает:

```text
poll listPlayers every minute
```

---

# 14. Частый tracking серверов

Для `tracking_enabled = true`:

```text
каждую минуту
```

примерный цикл:

```text
server
  ↓
rooms/search(hostAddress)
  ↓
resolve current roomId
  ↓
rooms/listPlayers(roomId)
  ↓
connectedPlayers + queuePlayers
  ↓
save observation
  ↓
diff against previous successful observation
  ↓
update sessions / aliases / identities
  ↓
emit domain events
```

Можно оптимизировать позже.

Сначала важнее корректность.

---

# 15. Watchlist

> **Пересмотрено 2026-09-11 (см. §2.4).** Таблицы подписок в observer **не создаются**: подписки, чаты и правила
> живут в телеграм-боте (проект `teamspeakbot`), который тянет ленту `GET /events` по своему курсору.
> Дедупликация решается там же курсором: событие с `id ≤ курсор` второй раз не придёт, даже после перезапуска бота.
> Требования ниже остаются как требования **к потребителю** ленты.

Минимальные таблицы (если когда-нибудь понадобится свой watchlist внутри observer):

```text
watchlist
---------
id
owner_user_id
player_id
enabled
created_at
```

```text
notification_rule
-----------------
id
watchlist_id
event_type
server_id nullable
enabled
```

Пример event type:

```text
PLAYER_JOINED_SERVER
```

Позже:

```text
PLAYER_LEFT_SERVER
PLAYER_ENTERED_QUEUE
PLAYER_LEFT_QUEUE
PLAYER_NICKNAME_CHANGED
```

Не отправлять notification при:
- startup replay — события помечены `startup_replay` и в ленту по умолчанию не отдаются;
- poll recovery после большой data gap без достаточного доказательства — события помечены `after_data_gap`;
- повторном poll того же состояния — событие вообще не порождается, событий «состояние не изменилось» нет.

Нужна idempotency/deduplication — обеспечивается монотонным `domain_event.id` и курсором потребителя.

---

# 16. Notification transports

> **Пересмотрено 2026-09-11.** Транспортов внутри observer нет и не планируется: он не знает ни про Telegram,
> ни про Discord. Единственный интерфейс наружу — REST (`docs/api.md`). Правило «domain/event слой не знает
> детали Telegram API» выполнено в максимально сильной форме: про них не знает весь сервис.
> Если появится второй потребитель (web, Discord-бот), он просто ведёт свой курсор по той же ленте.

---

# 17. Социальная аналитика

Сырая основа — presence sessions.

Не вычислять социальный граф напрямую из текущего списка.

Основные метрики:

```text
shared_minutes
shared_session_count
same_join_window_count
same_leave_window_count
same_server_transition_count
different_servers_together
association_score
```

Пример сильного сигнала:

```text
A и B 35 раз вошли на сервер с разницей < 3 минут
```

сильнее, чем:

```text
A и B один раз 4 часа были на сервере с 128 игроками
```

Параметры окон должны быть конфигурируемыми.

Например:

```text
join_window = 3 min
leave_window = 3 min
```

---

# 18. Графы

Нужны минимум два типа графов.

## 18.1. Player ↔ Server

Двудольный граф.

Node types:
- Player;
- Server.

Edge:
- игрок посещал сервер.

Edge weight:
- total play time;
- visit count;
- recency;
- queue visits.

---

## 18.2. Player ↔ Player

Ребро означает co-presence / association.

Вес:
- association_score;
- shared_minutes;
- joint join count;
- joint leave count.

Не называть ребро friendship.

---

# 19. Frontend графов

Для MVP:

```text
D3.js + SVG
```

Нужно:
- pan;
- zoom;
- drag nodes;
- фильтрация по времени;
- выбор метрики веса;
- раскрытие соседей;
- player details;
- aliases;
- посещённые servers;
- server details;
- возможность экспортировать SVG.

Для сотен узлов SVG достаточно.

Если граф вырастет до десятков тысяч nodes / сотен тысяч edges:
- backend API не менять;
- renderer можно заменить на Sigma.js/WebGL или аналог.

---

# 20. Предпочтительная backend архитектура

Один backend с внутренними модулями:

```text
ServerCatalog
PlayerTracking
Observation
Analytics
Watchlist
Notifications
Infrastructure
```

Пример зависимостей:

```text
Infrastructure -> external BI API / DB / token service
Observation -> creates normalized observations
PlayerTracking -> consumes observations
ServerCatalog -> owns local servers
Analytics -> reads historical sessions
Watchlist -> consumes domain events
Notifications -> transports
```

Не превращать это в микросервисы на старте.

---

# 21. Предпочтительная БД

На старте подходит обычная SQL БД:

```text
PostgreSQL
```

или:

```text
MariaDB
```

Выбор не является архитектурно критичным.

Если проект создаётся с нуля и нет внешнего ограничения, PostgreSQL предпочтителен из-за:
- хороших JSON возможностей;
- удобных аналитических запросов;
- оконных функций;
- расширяемости.

Но не усложнять проект сменой БД, если уже выбран MariaDB.

---

# 22. API backend

> **Реализовано (контракт — [`docs/api.md`](docs/api.md)):** `GET /events` (курсор `after`, фильтры
> `player_ids`/`types`, скрытие `startup_replay`), `GET /events/head`, `GET /players?nick=` (поиск по любому
> когда-либо наблюдённому нику), `GET /players/{id}`, `GET /players/{id}/sessions`, `GET /servers`,
> `GET /health`, `GET /observation-status`. Авторизация — `Authorization: Bearer $API_TOKEN`;
> без токена в конфиге API отключён. Порт слушает только `127.0.0.1`.
>
> Ещё не реализовано из списка ниже: `/servers/{id}/history`, `/graph/*`, `/watchlist` (не нужен, см. §15).

Минимальные направления:

```http
GET /servers
GET /servers/{id}
GET /servers/{id}/players
GET /servers/{id}/history

GET /players
GET /players/{id}
GET /players/{id}/aliases
GET /players/{id}/sessions
GET /players/{id}/servers

GET /graph/player-server
GET /graph/player-player

GET /watchlist
POST /watchlist
DELETE /watchlist/{id}

GET /health
GET /observation-status
```

Конкретные формы контрактов определить при реализации.

---

# 23. MVP приоритет

> **Статус 2026-09-11.** Пункты 1–11 списка ниже сделаны (watchlist — в виде REST-ленты для внешнего бота, §2.4).
> Первый прод-запуск: 2026-09-10, 107 серверов. Первые сутки: ~85 000 запросов к Bohemia, 97% успешных,
> признаков ограничений со стороны Bohemia нет; 40 000 игроков, 59 400 сессий. Критерий «неделя непрерывного
> сбора без ложных входов/выходов» — в процессе проверки.


Первый milestone — **не frontend**.

Первый настоящий критерий успеха:

> сервис минимум неделю непрерывно собирает данные с 5–10 выбранных серверов, корректно переживает network/API/auth failures, не создаёт ложных входов/выходов и правильно ведёт identities + aliases + sessions.

Порядок:

```text
1. BI client
2. token integration
3. server catalog
4. tracking_enabled servers
5. listPlayers collector
6. reliable poll_run
7. identity + alias
8. presence sessions
9. queue sessions
10. derived events
11. watchlist notifications
12. analytics
13. frontend
14. graphs
```

Frontend можно начать раньше только если он нужен для ручного управления catalog/tracking.

---

# 24. Наблюдаемость самого сервиса

Backend должен иметь собственные метрики/health.

Минимум:
- last successful global lobby scan;
- last successful poll per server;
- poll success ratio;
- auth failures;
- network failures;
- invalid responses;
- active presence sessions;
- current tracked server count;
- current known player count;
- queue player count;
- notification delivery errors.

Не полагаться только на текстовые логи.

**Реализовано:** `GET /observation-status` отдаёт время и статус последнего скана, счётчики серверов/игроков/
сессий/необработанных событий, разбивку опросов за час по статусам и по каждому отслеживаемому серверу —
возраст данных (`data_age_seconds`), статус последнего опроса, игроков и очередь из последнего успешного.
Логи — JSON в stdout и в файл `logs/observer.log` с ротацией (переживает пересоздание контейнера).
Prometheus-метрики пока не подключены: те же числа берутся из `poll_run` и `/observation-status`.

---

# 25. Важные правила реализации

## 25.1. Не выдумывать факты

Если API не ответил — это unknown, не offline.

Если игрок отсутствует только в одном observation — это ещё не обязательно leave.

Если faction неизвестен — не делать вывод о faction.

Если связь игроков корреляционная — не называть её дружбой или координацией.

---

## 25.2. Сохранять историю

Не затирать:
- старые nickname;
- старые platform mappings;
- исчезнувшие servers;
- прошлые roomId;
- старые server states.

Проект ценен именно временной историей.

---

## 25.3. Internal ID отдельно от external IDs

Везде использовать внутренний PK.

Например:

```text
player.id
server.id
```

Внешние ID хранить как атрибуты:

```text
bohemia_user_id
platform_user_id
room_id
host_address
```

Это позволяет переживать изменение внешней модели.

---

## 25.4. Отделять "observed" и "derived"

Observed:
- API ответил;
- player присутствовал;
- nickname был X.

Derived:
- player joined;
- player left;
- nickname changed;
- association increased.

Никогда не смешивать эти уровни в одной сущности без необходимости.

---

# 26. Отдельно исследовать позже

Не включать в MVP, но не забыть.

## 26.1. Queue position

> **Частично закрыто 2026-09-11:** порядок `queuePlayers[]` с очерёдностью не связан (см. §3.2), позицию из
> lobby API получить нельзя. Остальные вопросы этого раздела (где реализован VIP-приоритет, отдаёт ли
> `rooms/join` позицию) открыты.


У пользователя есть VIP/priority access на одном сервере, который ставит его первым в очередь.

Нужно экспериментально проверить:
- соответствует ли порядок `queuePlayers[]` реальной позиции;
- является ли массив отсортированным;
- есть ли отдельный endpoint состояния собственной queue;
- возвращает ли `rooms/join` position;
- где технически реализован VIP queue priority.

Изменение позиции очереди / управление проходкой — отдельная задача.
Возможно это:
- RCON;
- server-side mod;
- dedicated server API;
- backend/S2S.

Не смешивать управление сервером с observer.

---

## 26.2. Better identity enrichment

Исследовать:
- совпадает ли `listPlayers.userId` с официальным Game Identity/identityId;
- можно ли получить BI account relation;
- можно ли получить Steam profile safely/legally при необходимости;
- возможна ли 1 SteamID ↔ несколько Bohemia Game Identity;
- возможна ли связь разных platforms с одним человеком.

Не объединять identities автоматически без доказательств.

---

## 26.3. Server identity

> **Закрыто 2026-09-11:** `roomId` переживает смену адреса и меняется при рестарте; `hostAddress`
> переиспользуется хостингом; `sessionId` не уникален; `directJoinCode` нестабилен. Резолюция — `ROOM_ID`,
> затем `HOST_ADDRESS` (§2.1, ADR 0004). Полей `dedicatedServerId` / `providerServerId` в ответе `rooms/search` нет.


Исследовать стабильность:
- roomId;
- hostAddress;
- sessionId;
- providerServerId;
- dedicatedServerId.

Это критично для корректной долгосрочной server history.

---

## 26.4. Player faction/team

Текущий public/internal listPlayers этого не даёт.

Возможные будущие источники:
- собственный Arma Reforger mod;
- server logs;
- RCON;
- Enfusion Script API;
- S2S/backend traffic.

Это расширит social/anti-abuse analytics.

---

# 27. Связь с будущим Arma Reforger stats mod

Этот observer может работать без модов.

Позже возможен отдельный mod, который даст:
- kills;
- deaths;
- vehicle destruction;
- occupants of destroyed vehicles;
- driver/pilot/gunner context;
- faction;
- squad;
- session details;
- mass casualty events;
- temporal combat analytics.

Observer и mod должны интегрироваться через явный контракт/API, но не становиться одним монолитом по смыслу.

Observer отвечает за:
- внешнее наблюдение;
- присутствие;
- identities;
- server history;
- social graph.

Mod отвечает за:
- внутриигровые события, которых нет в lobby API.

---

# 28. Технический стиль работы

При разработке:
- сначала минимальный работающий путь;
- не усложнять заранее;
- один источник истины на bounded context;
- конфигурацию протокольных констант вынести из кода;
- писать тесты на parsing undocumented JSON;
- хранить fixtures реальных Bohemia responses;
- все parser должны спокойно переживать новые неизвестные поля;
- отсутствующее поле не должно ломать весь poll, если оно не критично;
- критичные protocol changes должны быть заметны в логах/metrics;
- обязательно idempotent processing.

---

# 29. Первые реальные тестовые данные

Из `listPlayers` уже получены реальные объекты:

```json
{
  "userId": "4537e0d4-f960-46ac-bafc-a0ad390b41ea",
  "username": "Salat Majompski",
  "gameClientType": "PLATFORM_PC",
  "platformUserId": "76561198884181842"
}
```

и:

```json
{
  "userId": "a814bb9c-e6a2-4518-8673-16251edb1c4c",
  "username": "42Трусиля₄³XXL42",
  "gameClientType": "PLATFORM_PC",
  "platformUserId": "76561199250528863"
}
```

Тестовый публичный сервер, использовавшийся при исследовании:

```text
[RU] #1 | ARMA-RUSSIAN.RU | RUSSIAN SERVER | VANILLA CONFLICT EVERON
hostAddress: 37.48.253.41:2001
roomId: 34008e3e-9a0c-4be0-b94f-c7f3cb547626
```

Этот roomId считать только текущим примером, не постоянной конфигурацией.

---

# 30. Главное архитектурное резюме

Проект должен быть простым:

```text
Bohemia API
    ↓
Collector
    ↓
Observations
    ↓
SQL DB
    ↓
Sessions / Events
    ↓
Analytics + Watchlist
    ↓
Backend API
    ↓
Frontend
```

Главные принципы:

0. Факты о недокументированном API — только из наблюдений, и они записываются в `docs/research`.
   Гипотеза без проверки не попадает в код (так были опровергнуты «порядок очереди» и «roomId уникален для сервера»).
1. Полностью отдельно от `ts-monitoring`.
2. `Bohemia userId` — основной внешний player identity.
3. `platformUserId` — важная дополнительная platform identity.
4. Nickname — только alias.
5. Не путать отсутствие данных с отсутствием игрока.
6. Хранить историю и интервалы неопределённости.
7. Сначала надёжный collector, потом красивые графы.
8. Социальные выводы делать из повторяемых паттернов, а не из единичных совпадений.
9. Watchlist и transports не связывать напрямую с poller.
10. Не строить микросервисную архитектуру до появления реальной необходимости.
