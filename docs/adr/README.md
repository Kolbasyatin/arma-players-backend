# Architecture Decision Records

Формат: один файл на решение, `NNNN-kebab-title.md`. Статусы: `Proposed` → `Accepted` → (`Superseded by NNNN` | `Deprecated`).

Диаграммы — Mermaid в fenced-блоках. В GoLand: **Settings → Languages & Frameworks → Markdown → включить Mermaid** (в свежих версиях включён по умолчанию), затем Markdown preview.

| # | Решение | Статус |
|---|---------|--------|
| [0001](0001-modular-monolith.md) | Один Go-бинарник с внутренними модулями | Accepted |
| [0002](0002-project-layout-and-libraries.md) | Раскладка Go-проекта и базовые библиотеки | Accepted |
| [0003](0003-postgresql-storage.md) | PostgreSQL, pgx, SQL-миграции | Proposed |
| [0004](0004-server-identity-and-merge.md) | Логическая идентичность сервера и merge истории | Proposed |
| [0005](0005-observation-pipeline-and-presence.md) | Observation → Diff → Derived events; семантика неудачного poll | Accepted |
| [0006](0006-player-identity-and-aliases.md) | Идентичность игрока: bohemia_user_id, platform, alias | Accepted |
| [0007](0007-domain-events-and-outbox.md) | In-process события + outbox для watchlist | Proposed |
| [0008](0008-tracking-selection.md) | Выбор серверов для минутного опроса: MANUAL + AUTO по онлайну | Accepted |

Обязательный контекст для всех ADR — [`AGENTS.md`](../../AGENTS.md).
