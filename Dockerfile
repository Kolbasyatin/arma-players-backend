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
USER nonroot
EXPOSE 8081
ENTRYPOINT ["/observer"]
