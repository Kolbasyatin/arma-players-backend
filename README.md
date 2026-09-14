# armaplayers — Arma Reforger Player Observer (backend)

Наблюдает за публичными серверами Arma Reforger через lobby API Bohemia: каталог серверов,
присутствие игроков, история ников и платформенных id, сессии, очереди, derived-события.
Контекст и правила — [AGENTS.md](AGENTS.md), решения — [docs/adr](docs/adr/README.md),
факты о API — [docs/research](docs/research/bohemia-api-observations.md).

## Локально

```bash
docker compose up -d --wait            # Postgres на 127.0.0.1:5434
cp .env.example .env                   # заполнить TOKEN_URL (сервис arma-reforger-hz)
go test ./...                          # интеграционные тесты идут в DATABASE_URL_TEST
go run ./cmd/probe -host 37.48.253.41:2001   # ручная проверка протокола, без БД
go run ./cmd/observer                  # сервис: миграции → сканы → опрос → /observation-status
curl -s localhost:8081/observation-status | jq
```

## Запросы к API руками

`http/api.http` — JetBrains HTTP Client (GoLand: открыть и нажать ▶). Два окружения в
`http/http-client.env.json`: `local` (сервис на этой машине, порт 8081) и `prod` (через SSH-туннель).

```bash
cp http/http-client.private.env.json.example http/http-client.private.env.json   # сюда API_TOKEN
ssh -N -L 18081:127.0.0.1:8081 <пользователь>@<сервер>                            # для окружения prod
```

Приватный файл с токенами в `.gitignore`.

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
tail -f logs/observer.log | jq -c .
```

Логи — `logs/observer.log` рядом с приложением (JSON, ротация по размеру и сроку), плюс `docker logs`.
Данные Postgres — в docker volume `armaplayers_postgres-data`; бэкап: `docker exec armaplayers-postgres pg_dump -U armaplayers armaplayers | gzip > backup.sql.gz`.

Секреты живут только в `deploy/.env` (в `.gitignore`).
Миграции применяются при старте контейнера, остановка по SIGTERM корректная.

### Деплой по кнопке

`Actions → deploy → Run workflow`. Ничего не собирает: заходит по SSH на сервер и делает там
`git pull` (чтобы приехали правки `compose.yaml` и юнита), тянет образ из GHCR по указанному тегу,
записывает этот тег в `deploy/.env`, перезапускает юнит и **проверяет** `/health`, пока не ответит.
Зелёная галочка означает «выкачено и живо», а не «команда отправлена». Откат — тот же запуск
с тегом `sha-<коммит>`.

Что откуда берётся:

| Что | Источник |
|---|---|
| бинарник | образ из GHCR |
| `compose.yaml`, systemd-юнит, `.env.example` | **внутри того же образа**, достаются `docker create` + `docker cp` |
| `deploy/.env` с секретами | только на сервере, ни в git, ни в образе |

Git-клона на прод-сервере не нужно. Конфигурация едет вместе с образом, поэтому compose всегда
соответствует ровно выкатываемому тегу: откат на старый образ возвращает и его compose.
Новый compose проверяется до подмены, каталог заменяется целиком, ваш `.env` переносится как есть.
Systemd-юнит workflow не меняет (это вне прав деплоя), но о расхождении предупредит в логе
и подскажет команду.

### Первый запуск на чистой машине

```bash
sudo mkdir -p /opt/teamspeakbot/arma-players && cd /opt/teamspeakbot/arma-players
IMAGE=ghcr.io/kolbasyatin/arma-players-backend:latest
docker pull "$IMAGE"
cid=$(docker create "$IMAGE") && docker cp "$cid:/deploy/." ./deploy/ && docker rm "$cid"

cp deploy/.env.example deploy/.env && chmod 600 deploy/.env && $EDITOR deploy/.env
sudo cp deploy/armaplayers-observer.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now armaplayers-observer
```

Дальше всё обновление — кнопкой.

Настраивается один раз. На сервере:

```bash
# отдельный ключ только для деплоя
ssh-keygen -t ed25519 -f ~/.ssh/deploy_armaplayers -N "" -C "github-deploy"
cat ~/.ssh/deploy_armaplayers.pub >> ~/.ssh/authorized_keys

# рестарт без пароля — ровно одна команда, ничего больше
echo "$USER ALL=(root) NOPASSWD: /usr/bin/systemctl restart armaplayers-observer"   | sudo tee /etc/sudoers.d/armaplayers-deploy
sudo chmod 440 /etc/sudoers.d/armaplayers-deploy

# отпечаток хоста для known_hosts на стороне CI
ssh-keyscan -H <адрес сервера>
```

В `Settings → Secrets and variables → Actions` репозитория:

| Что | Тип | Значение |
|---|---|---|
| `DEPLOY_HOST` | Secret | адрес сервера |
| `DEPLOY_USER` | Secret | пользователь из команд выше |
| `DEPLOY_SSH_KEY` | Secret | содержимое `~/.ssh/deploy_armaplayers` целиком |
| `DEPLOY_KNOWN_HOSTS` | Secret | вывод `ssh-keyscan -H` |
| `DEPLOY_PATH` | Variable | каталог проекта, если он не `/opt/teamspeakbot/arma-players` |

Кнопку можно нажимать и не открывая браузер, из терминала или Run Configuration в IDE:

```bash
gh workflow run deploy.yaml -f tag=latest        # выкатить
gh workflow run deploy.yaml -f tag=sha-1a2b3c4   # откатиться
gh run watch                                      # смотреть за выполнением
```


## Бинарники

- `cmd/observer` — сервис.
- `cmd/probe` — разовые запросы к Bohemia для исследования (`-raw` печатает сырые ответы).
