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
        text key_type "HOST_ADDRESS | ROOM_ID | SESSION_ID | DIRECT_JOIN_CODE | DEDICATED_SERVER_ID | PROVIDER_ID"
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

`roomId` — идентификатор **текущей игровой сессии** сервера в лобби, а не сервера. Мы получаем его как результат поиска (например, по `hostAddress`) и используем только как параметр для `listPlayers`. В резолюции идентичности он не участвует; в `server_identity_key` он записывается лишь как история («какие сессии были у сервера»).

```mermaid
flowchart TD
    R[room из rooms/search] --> K1{стабильный ключ хоста\nDEDICATED_SERVER_ID / PROVIDER_ID\nесть в payload и уже известен?}
    K1 -- да --> S[server найден]
    K1 -- нет --> K2{HOST_ADDRESS уже известен?}
    K2 -- да --> S
    K2 -- нет --> NEW[создать новый server\n+ SERVER_DISCOVERED]
    NEW --> SUG[посчитать кандидатов\nна merge → server_match_suggestion]
    S --> UPD[обновить identity keys, last_seen,\ncurrent_room_id = id текущей сессии]
```

Приоритет ключей сверху вниз. Какие поля из `rooms/search` реально стабильны (`hostAddress`, скрытые id провайдера/dedicated server) — предмет отдельного исследования (AGENTS.md §26.3); список `key_type` расширяемый, приоритеты конфигурируемы.

Для tracking-цикла сервер ищется по `current_host_address` → получаем актуальный `roomId` → `listPlayers(roomId)`. При `ROOM_NOT_FOUND` **не** переключаемся автоматически на другой адрес, а фиксируем poll_run с ошибкой и даём match-подсказку.

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
3. совпадение `directJoinCode`, если он переживает переезд;
4. совпадение имени + сценария + набора модов + `playerCountLimit`;
5. совпадение только имени — слабый признак, сам по себе недостаточен.

Формулы, пороги и веса **не фиксируются** до появления реальных данных каталога.

## Последствия

- Одинокая запись сервера без истории — нормальное состояние; связывать её с прошлым будет оператор по подсказке.
- Индексы: `server_identity_key(key_type, key_value)` для быстрой резолюции; уникальность на активный HOST_ADDRESS не вводим до подтверждения данными.
- Все запросы аналитики обязаны учитывать canonical id — это должно быть в одном месте (SQL-функция или репозиторий), а не размазано по коду.

## Открытые вопросы

- Подтвердить экспериментом, что `roomId` меняется при перерегистрации (снять до/после рестарта известного сервера).
- Есть ли в payload `rooms/search` стабильный id хоста (dedicated server id / provider id).
- Стабильность `directJoinCode` и `sessionId`.
