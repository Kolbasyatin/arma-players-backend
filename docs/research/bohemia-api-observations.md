# Наблюдения за Bohemia lobby API

Журнал фактов с живого API. Дата, что наблюдали, что из этого следует. Гипотезы помечены явно.
Связанные открытые вопросы — ADR 0004 (идентичность сервера), AGENTS §26.

## 2026-09-10 — первый живой запуск `cmd/probe`

Сервер `37.48.253.41:2001` («[RU] #1 | ARMA-RUSSIAN.RU»), клиент 1.8.0.13.

**rooms/search по hostAddress**
- Вернул ровно одну комнату, `totalCount=1`, `searchFrom=0` совпал с запросом. Гипотеза «несколько комнат на адрес» пока не подтверждена; `Limit: 5` оставлен как страховка.
- Полный набор полей комнаты при `lightweight=false` (26 ключей): все поля из AGENTS §3.1 плюс `detailsUpdatedAt`, `favorite`, `flags`. Добавлены в `bohemia.Room`.
- `roomId = 34008e3e-9a0c-4be0-b94f-c7f3cb547626` — **тот же**, что в fixture от 2026-09-03 и в AGENTS.md. Минимум неделю roomId не менялся. Либо сервер не перезапускался, либо roomId переживает рестарт. Требует целевого эксперимента: снять roomId до и после известного рестарта.
- `directJoinCode`: 2026-09-03 было `0956251811`, сейчас `0245999839`. **Меняется в пределах недели** → не годится как identity key (ADR 0004, кандидат понижен).
- `sessionId = c134568a-000051ffebe3` — формат отличается от UUID; сравнить при следующих запусках.
- `hostType = CommunityDs`, `official = false`, `battlEye = true`, `platformName = Windows`, `pingSiteId = frankfurt`.
- `supportedGameClientTypes = [PLATFORM_PC, PLATFORM_XBL, PLATFORM_PSN]` — кроссплатформенный сервер.
- Два таймстампа: `updated` (heartbeat, менялся) и `detailsUpdatedAt` (описание).
- `mods = []` — форма элементов по-прежнему неизвестна, нужен модовый сервер.
- Очередь: `joinQueue = {type: REGULAR, size: 19, maxSize: 50, positionAvgWaitTime: 70}`.

**rooms/listPlayers**
- 128 connected + 19 queue, числа совпали с `playerCount` и `joinQueue.size` из search.
- `gameClientType` не только `PLATFORM_PC`: наблюдались `PLATFORM_PSN` (platformUserId — 19-значное число) и `PLATFORM_XBL` (40 hex-символов). Подтверждает ADR 0006: `platform_user_id` — текст, без предположений о формате; `game_client_type` обязателен в ключе маппинга.
- Bohemia `userId` — UUID для всех платформ.

**Следующие проверки**
- roomId после рестарта сервера (нужен сервер с известным временем рестарта или наблюдение за `sessionId`/`updated`).
- Ответ `listPlayers` на несуществующий/устаревший roomId — какой HTTP-статус (для оптимизации «сначала старый roomId»).
- Максимальный `limit` и поведение пагинации при полном скане (`lightweight=true`).
- Порядок `queuePlayers[]` — соответствует ли позиции в очереди.
