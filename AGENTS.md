# AGENTS.md — Arma Reforger Player Observer

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

Раз в сутки выполняется полный скан публичного lobby Arma Reforger.

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

Не предполагать заранее, что `roomId` является вечным идентификатором физического сервера.

Нужно отдельно исследовать:
- переживает ли `roomId` рестарт;
- меняется ли он при новой игровой сессии;
- что является самым устойчивым server identity:
  - hostAddress;
  - dedicated server id;
  - provider id;
  - комбинация полей.

До подтверждения не строить важные связи только на `roomId`.

---

## 2.2. Tracking игроков

Для серверов, включённых в tracking, делается частый polling.

Начальная целевая частота:

```text
1 раз в минуту
```

Это не жёсткая константа архитектуры. Интервал должен быть конфигурируемым.

Каждый успешный poll должен:
1. найти актуальную room;
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

Далее отдельный слой сопоставляет событие с подписками и отправляет notification.

Транспорт доставки не должен быть частью логики tracking.

Потенциальные transports:
- Telegram;
- Discord;
- Email;
- Web Push;
- другие.

Архитектура:

```text
Collector
   ↓
Domain Event
   ↓
Watchlist Matcher
   ↓
Notification Transport
```

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
size
maxSize
positionAvgWaitTime
```

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

Возможный порядок `queuePlayers` как фактический порядок очереди ещё нужно исследовать экспериментально.

Не считать это доказанным, пока не подтверждено отдельным тестом.

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

Не считать `current_room_id` вечным identity.

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

Минимальные таблицы:

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
- startup replay;
- poll recovery после большой data gap без достаточного доказательства;
- повторном poll того же состояния.

Нужна idempotency/deduplication.

---

# 16. Notification transports

Абстракция транспорта должна быть отдельной.

Пример:

```text
NotificationTransport
```

реализации:

```text
TelegramTransport
DiscordTransport
EmailTransport
WebPushTransport
```

Domain/event слой не знает детали Telegram API или Discord webhook.

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

# 22. API будущего backend

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
