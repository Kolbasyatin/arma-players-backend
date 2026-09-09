# ADR 0002 — Раскладка Go-проекта и базовые библиотеки

**Статус:** Proposed · **Дата:** 2026-09-07

## Раскладка

```text
cmd/
  observer/            main.go — wiring, запуск фоновых циклов и HTTP
internal/
  app/                 сборка зависимостей, lifecycle, graceful shutdown
  config/              env-конфигурация, включая протокольные константы BI
  bohemia/             клиент Bohemia API: search rooms, listPlayers, DTO, парсеры
    testdata/          fixtures реальных ответов
  token/               клиент arma-reforger-hz, кэш, инвалидация на 401/403
  catalog/             ServerCatalog: discovery scan, servers, identity keys, merge, server_event
  tracking/            планировщик poll'ов для tracking_enabled серверов
  observation/         poll_run, normalized observation, raw payload retention
  presence/            diff engine, player_identity, platform identity, alias, sessions, queue
  events/              типы доменных событий, in-process bus, outbox
  watchlist/           watchlist, notification_rule, matcher, dedup
  notify/              NotificationTransport + реализации (telegram, discord, ...)
  analytics/           co-presence, association metrics, graph queries
  httpapi/             REST handlers, DTO ответов, middleware
  storage/
    postgres/          pgxpool, репозитории по модулям
    migrations/        SQL-миграции, embed
  metrics/             prometheus registry, health snapshot
docs/adr/
```

Правила:

- Каждый доменный модуль экспортирует небольшой `Service` и интерфейсы репозиториев. Реализации репозиториев лежат в `storage/postgres`, чтобы домен не зависел от pgx.
- Доменные типы (`Server`, `PlayerIdentity`, `PresenceSession`, события) живут в своём модуле, не в общем `models`.
- Никаких `pkg/`, `utils/`, `common/` на старте.

## Библиотеки

| Область | Выбор | Почему |
|---------|-------|--------|
| HTTP сервер/роутер | `net/http` stdlib (Go ≥ 1.22 method patterns) | Хватает для REST, нет лишней зависимости |
| HTTP клиент | `net/http` + свой транспорт с таймаутами, retry только для идемпотентных вызовов | Нужна тонкая классификация ошибок (DNS/TLS/timeout) |
| БД | `jackc/pgx/v5` + `pgxpool` | Нативный Postgres, типы, batch |
| Миграции | `pressly/goose/v3`, SQL-файлы через `embed` | Простые, читаемые SQL-миграции |
| Конфиг | `caarlos0/env/v11` | env-only конфигурация, 12-factor |
| Логи | `log/slog` (JSON) | Stdlib, структурные логи |
| Метрики | `prometheus/client_golang` | Требование AGENTS.md §24 |
| Lifecycle | `golang.org/x/sync/errgroup` | Несколько фоновых циклов в одном процессе |
| Тесты | `testing` + `stretchr/testify` | Fixtures реальных JSON в `testdata` |
| Линт | `golangci-lint` с `depguard` для границ модулей | Защита архитектуры ADR 0001 |

Отложено: `sqlc` (если рукописных запросов станет много), `robfig/cron` (сутки достаточно тикера + проверки «последний скан старше N часов»).

## Go toolchain

Целевая версия: последняя стабильная на момент старта кода. На машине разработчика `go` не в PATH; SDK GoLand — уточнить перед `go mod init`.

## Открытые вопросы

- Имя модуля: `github.com/<org>/reforger-observer-backend` или текущее имя репозитория `arma-players-backend`.
