# ADR 0004 — Логическая идентичность сервера и merge истории

**Статус:** Proposed · **Дата:** 2026-09-07

## Контекст

Ручная проверка владельца проекта: `roomId` выдаётся заново при каждой регистрации сервера в лобби (в частности после рестарта). Это будет перепроверено отдельным экспериментом, но проектируем исходя из того, что **roomId — идентификатор игровой сессии, а не сервера**.

Сервер как объект наблюдения может:

- переехать на другой `hostAddress`, сохранив имя;
- сменить имя, сохранив адрес;
- сменить сценарий, моды, лимит игроков, directJoinCode;
- исчезнуть на дни и вернуться.

Ценность проекта — многолетняя история посещений. Её нельзя рвать при переезде, но и нельзя автоматически склеивать разные серверы по совпадению имени.

## Решение

### 1. Три уровня сущностей

```mermaid
erDiagram
    server ||--o{ server_identity_key : "наблюдённые ключи"
    server ||--o{ server_observation : "состояния"
    server ||--o{ server_event : "изменения"
    server ||--o{ player_server_session : "посещения"
    server ||--o| server : "merged_into"
    server ||--o{ server_merge : "source / target"
    server ||--o{ server_match_suggestion : "кандидаты"

    server {
        bigint id PK
        bigint merged_into_server_id FK "NULL = канонический"
        text display_name
        text current_host_address
        text current_room_id "id текущей сессии, не identity"
        bool tracking_enabled
        bool active
        timestamptz first_seen_at
        timestamptz last_seen_at
    }
    server_identity_key {
        bigint id PK
        bigint server_id FK
        text key_type "HOST_ADDRESS | ROOM_ID | SESSION_ID (история, не identity)"
        text key_value
        timestamptz first_seen_at
        timestamptz last_seen_at
        int observation_count
    }
    server_merge {
        bigint id PK
        bigint source_server_id FK
        bigint target_server_id FK
        timestamptz merged_at
        text merged_by
        text reason
        timestamptz unmerged_at "NULL = активен"
    }
    server_match_suggestion {
        bigint id PK
        bigint server_a_id FK
        bigint server_b_id FK
        numeric score
        jsonb evidence
        text status "OPEN | ACCEPTED | REJECTED"
        timestamptz created_at
    }
```

- **`server`** — логический сервер, к которому привязана история. Каноническим считается запись с `merged_into_server_id IS NULL`.
- **`server_identity_key`** — все наблюдённые внешние ключи с интервалами жизни. Отвечает на вопрос «какие адреса/roomId/коды когда-либо принадлежали серверу».
- **`server_merge`** — журнал операций схлопывания, обратимый.

### 2. Резолюция room → server при наблюдении

**Пересмотрено 2026-09-11 по данным первых суток** (см. `docs/research/bohemia-api-observations.md`): за сутки 129 серверов сменили порт или IP, **сохранив** `roomId` и `sessionId`, и только 18 сменили `roomId` при рестарте. Адреса хостинги переиспользуют, `roomId` (UUID) — нет. Поэтому резолюция идёт **сначала по `ROOM_ID`, затем по `HOST_ADDRESS`**: совпал roomId — это тот же сервер, даже если адрес другой; roomId неизвестен, но адрес известен — рестарт того же сервера. Не найден ни один — новый сервер. Дубли, созданные до этого правила, схлопываются автоматически (`MergeDuplicateRooms` после скана, запись в `server_merge`, `merged_by = auto`).

```mermaid
flowchart TD
    R[room из rooms/search] --> K1{ROOM_ID уже известен?}
    K1 -- да --> S[server найден\nадрес обновлён]
    K1 -- нет --> K2{HOST_ADDRESS уже известен?}
    K2 -- да --> S
    K2 -- нет --> NEW[создать новый server\n+ SERVER_DISCOVERED]
    NEW --> SUG[посчитать кандидатов\nна merge → server_match_suggestion]
    S --> UPD[обновить identity keys, last_seen,\ncurrent_room_id = id текущей сессии]
```

Для tracking-цикла: сервер ищется по `current_host_address` → актуальный `roomId` → `listPlayers`. Если по адресу комнаты нет (`ROOM_NOT_FOUND`), пробуем `listPlayers` по сохранённому `current_room_id`: при переезде адреса он жив, и сессии игроков не рвутся; адрес обновит следующий скан лобби через резолюцию по ROOM_ID. Если и roomId мёртв (рестарт с новым roomId) — фиксируем `ROOM_NOT_FOUND`, состояние игроков не трогаем.

### 3. Мягкий merge

- Исторические строки (`server_observation`, `player_server_session`, `server_event`) **никогда не переписываются**: `server_id` остаётся указывать на исходную запись.
- Merge = проставить `source.merged_into_server_id = target.id`, перенести `tracking_enabled` на target, записать `server_merge`. Цепочки не допускаются: при merge в уже слитый сервер указатель ставится сразу на канонический.
- Все чтения (API, аналитика, watchlist по серверу) идут через функцию/представление `canonical_server_id(server_id)`.
- Unmerge = `unmerged_at` + обнулить указатель. История автоматически «расклеивается».
- Merge и unmerge — ручные операции через API/админку. Автоматический merge на MVP запрещён.

### 4. Признаки «вероятно тот же сервер» (решить позже)

Кандидаты для `server_match_suggestion.evidence`, порядок — по ожидаемой силе:

1. пересечение состава игроков: доля игроков нового сервера, которых видели на старом за последние N дней (Jaccard / overlap coefficient);
2. временная смежность: старый исчез ≈ когда появился новый;
3. ~~совпадение `directJoinCode`~~ — **снято 2026-09-10**: код сменился в пределах недели без переезда (см. `docs/research/bohemia-api-observations.md`);
4. совпадение имени + сценария + набора модов + `playerCountLimit`;
5. совпадение только имени — слабый признак, сам по себе недостаточен.

Формулы, пороги и веса **не фиксируются** до появления реальных данных каталога.

## Последствия

- Одинокая запись сервера без истории — нормальное состояние; связывать её с прошлым будет оператор по подсказке.
- Индексы: `server_identity_key(key_type, key_value)` для быстрой резолюции; уникальность на активный HOST_ADDRESS не вводим до подтверждения данными.
- Все запросы аналитики обязаны учитывать canonical id — это должно быть в одном месте (SQL-функция или репозиторий), а не размазано по коду.

## Открытые вопросы

- Подтвердить экспериментом, что `roomId` меняется при перерегистрации (снять до/после рестарта известного сервера). Наблюдение 2026-09-10: roomId тестового сервера не менялся минимум 7 дней.
- Есть ли в payload `rooms/search` стабильный id хоста (dedicated server id / provider id).
- ~~Стабильность `sessionId`~~ — **снято 2026-09-10**: sessionId не уникален (один у 22 комнат, другой у 2 разных серверов), как identity key не годится. Возможно, ключ уровня хоста — отдельная гипотеза.
- `directJoinCode` нестабилен — подтверждено 2026-09-10.
- ~~единственный пригодный ключ — hostAddress~~ — **2026-09-11:** `roomId` переживает смену адреса (129 случаев за сутки) и меняется при рестарте (18 случаев); `hostAddress` переиспользуется. Оба ключа нужны, roomId приоритетнее.
