# ADR 0005 — Observation → Diff → Derived events; семантика неудачного poll

**Статус:** Accepted · **Дата:** 2026-09-07

## Контекст

Bohemia отдаёт факты наблюдения дискретно. Сеть, токен и сам backend ненадёжны. Фундаментальное правило (AGENTS.md §2.5, §10): **«мы перестали получать данные» ≠ «игрок вышел»**.

## Решение

### Конвейер одного poll

```mermaid
sequenceDiagram
    participant T as tracking.Scheduler
    participant B as bohemia.Client
    participant O as observation
    participant P as presence.Diff
    participant E as events.Outbox
    participant DB as PostgreSQL

    T->>O: begin poll_run(server)
    T->>B: rooms/search(hostAddress)
    alt room не найдена / ошибка
        B-->>T: error(kind)
        T->>O: poll_run.status = ROOM_NOT_FOUND | DNS_FAILED | ...
        Note over P: состояние сессий НЕ меняется
    else ok
        B-->>T: roomId
        T->>B: rooms/listPlayers(roomId)
        alt ошибка
            B-->>T: error(kind)
            T->>O: poll_run.status = AUTH_ERROR | HTTP_ERROR | INVALID_JSON | ...
        else ok
            B-->>T: connected[], queue[]
            T->>DB: BEGIN
            T->>O: save observation (+ raw payload)
            O->>P: diff(prev successful observation, current)
            P->>P: update identities / aliases / sessions / queue sessions
            P->>E: append derived events
            T->>O: poll_run.status = SUCCESS
            T->>DB: COMMIT
        end
    end
```

Один poll = одна транзакция. Если упало на середине — ни observation, ни события не сохраняются, следующий poll сравнит с предыдущим успешным.

### Классификация ошибок

Клиент `bohemia` возвращает типизированную ошибку с `Kind` из фиксированного списка (`DNS_FAILED`, `CONNECTION_TIMEOUT`, `TLS_ERROR`, `AUTH_ERROR`, `HTTP_ERROR`, `BOHEMIA_ERROR`, `INVALID_JSON`, `INVALID_RESPONSE`, `ROOM_NOT_FOUND`, `TOKEN_UNAVAILABLE`). `AUTH_ERROR` дополнительно инвалидирует кэш токена.

### Состояние presence-сессии

```mermaid
stateDiagram-v2
    [*] --> ONLINE : успешный poll, игрок в connected
    ONLINE --> ONLINE : успешный poll, присутствует
    ONLINE --> SUSPECTED_GONE : успешный poll, отсутствует (1-й раз)\nfirst_known_absent_at = now
    SUSPECTED_GONE --> ONLINE : успешный poll, снова присутствует\nfirst_known_absent_at = NULL
    SUSPECTED_GONE --> CLOSED_LEFT : отсутствует N-й раз подряд (N конфиг, default 2)\nended_at = first_known_absent_at\nemit PLAYER_LEFT_SERVER
    ONLINE --> CLOSED_DATA_GAP : gap без успешных poll > max_gap (конфиг)\nended_at = last_seen_at, без PLAYER_LEFT
    SUSPECTED_GONE --> CLOSED_DATA_GAP : gap > max_gap
    state "failed poll" as F
    note right of F : любая ошибка poll —\nни один переход не выполняется
```

- `last_seen_at` — последний успешный poll с присутствием. `first_known_absent_at` — первый успешный poll с отсутствием. Между ними — интервал неопределённости, он хранится, а не угадывается.
- После большого разрыва данных (`CLOSED_DATA_GAP`) при возврате игрока открывается новая сессия, событие `PLAYER_JOINED_SERVER` помечается `after_data_gap = true`, чтобы watchlist мог решить, уведомлять ли.
- При старте процесса первый успешный poll — это **replay**: все присутствующие получают сессии, но события не эмитятся (или эмитятся с флагом `startup_replay = true`).

### Queue-сессии

Аналогичный автомат для `queuePlayers`. `result = JOINED_SERVER` выставляется, если игрок исчез из queue и появился в connected в пределах `queue_join_window` (конфиг) и без data gap между наблюдениями. Иначе `LEFT_QUEUE` или `UNKNOWN`.

### Observed vs derived

| Observed (факт) | Derived (вывод) |
|---|---|
| `server_observation`, `raw_payload` | `server_event` |
| присутствие игрока в observation | `player_server_session`, `PLAYER_JOINED/LEFT_SERVER` |
| `username` в observation | `player_alias`, `PLAYER_NICKNAME_CHANGED` |
| `queuePlayers` | `player_queue_session.result` |

Наблюдения не редактируются; выводы можно пересчитать заново из наблюдений (пока живы raw payload'ы).

## Последствия

- Frontend всегда может показать «данные не обновлялись N минут» из `poll_run`.
- Пороги `absent_confirmations`, `max_gap`, `queue_join_window`, `poll_interval` — конфигурация, не константы.
- Тесты diff engine — табличные, на последовательностях observation без сети.
