# syntax=docker/dockerfile:1
# Многоступенчатая сборка: компилируем в полном образе Go, запускаем в минимальном.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/observer ./cmd/observer && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/probe ./cmd/probe

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/observer /observer
COPY --from=build /out/probe /probe
# Конфигурация развёртывания едет ВНУТРИ образа: compose.yaml, systemd-юнит, шаблон .env.
# Сервер достаёт их из образа (docker create + docker cp), поэтому git-клона на проде не нужно,
# а compose гарантированно соответствует ровно этому образу — не вершине ветки, как было бы
# при git pull. Настоящий deploy/.env с секретами в образ не попадает: он в .dockerignore.
COPY deploy/ /deploy/
USER nonroot
EXPOSE 8081
ENTRYPOINT ["/observer"]
