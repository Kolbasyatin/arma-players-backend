# armaplayers — Arma Reforger Player Observer (backend)

Наблюдает за публичными серверами Arma Reforger через lobby API Bohemia: каталог серверов,
присутствие игроков, история ников и платформенных id, сессии, очереди, derived-события.
Контекст и правила — [AGENTS.md](AGENTS.md), решения — [docs/adr](docs/adr/README.md),
факты о API — [docs/research](docs/research/bohemia-api-observations.md).

## Локально

```bash
docker compose up -d --wait            # Postgres на 127.0.0.1:5433
cp .env.example .env                   # заполнить TOKEN_URL (сервис arma-reforger-hz)
go test ./...                          # интеграционные тесты идут в DATABASE_URL_TEST
go run ./cmd/probe -host 37.48.253.41:2001   # ручная проверка протокола, без БД
go run ./cmd/observer                  # сервис: миграции → сканы → опрос → /observation-status
curl -s localhost:8081/observation-status | jq
```

## Прод

Образ собирает GitHub Actions при push в `main` (и по тегам `v*`) и публикует в
`ghcr.io/kolbasyatin/arma-players-backend` с тегами `latest`, `sha-<commit>`, `<version>`.
Стек `deploy/compose.yaml` — Postgres + observer; сервис токена работает отдельно на хосте:

```bash
# на сервере, один раз
sudo git clone https://github.com/Kolbasyatin/arma-players-backend.git /opt/teamspeakbot/arma-players && cd /opt/teamspeakbot/arma-players
cp deploy/.env.example deploy/.env && chmod 600 deploy/.env && $EDITOR deploy/.env   # DATABASE_URL, TOKEN_URL, TRACK_*
# systemd управляет стеком: pull + up при старте/загрузке, down при stop; рестарт после аварии — docker
sudo cp deploy/armaplayers-observer.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now armaplayers-observer

# обновление до свежего образа
sudo systemctl restart armaplayers-observer
curl -s localhost:8081/observation-status | jq
docker logs -f armaplayers-observer
```

Данные Postgres — в docker volume `armaplayers_postgres-data`; бэкап: `docker exec armaplayers-postgres pg_dump -U armaplayers armaplayers | gzip > backup.sql.gz`.

Секреты живут только в `deploy/.env` (в `.gitignore`). Откат — `OBSERVER_TAG=sha-<commit>` в `.env` и `up -d`.
Миграции применяются при старте контейнера, остановка по SIGTERM корректная.

## Бинарники

- `cmd/observer` — сервис.
- `cmd/probe` — разовые запросы к Bohemia для исследования (`-raw` печатает сырые ответы).
