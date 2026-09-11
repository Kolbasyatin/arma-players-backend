# Журнал обучения Go

Статусы: ✅ объяснено · 🟡 частично · ⬜ не обсуждали · ➕ стоит обсудить для полноты темы.
Одна строка на тему. Подробности — по запросу.

## Раскладка проекта
- ✅ `internal/` — enforced компилятором: импорт только из поддерева родителя `internal`.
- ✅ `cmd/<name>/main.go` — только конвенция; `package main` + `func main()` делает бинарник, имя папки = имя бинарника при `go build`.
- ✅ Импорт только пакетом целиком, доступ `pkg.Name`; имя пакета из `package X` в исходниках, совпадает с последним элементом пути без `/vN`; alias при импорте. ⬜ неиспользуемый импорт = ошибка компиляции.
- ✅ Пакет = каталог; в модуле может быть много `package main` (по одному на каталог) или ни одного (библиотека).
- ✅ Имя файла компилятору безразлично, кроме суффиксов `_test.go` и платформенных `_linux.go` и т.п.
- ✅ `go run` vs `go build`; `./...`; почему нужен `./` (иначе аргумент = import path).
- ✅ `testdata/` — игнорируется `go build`, доступен тестам через относительный путь.
- 🟡 Каталоги, игнорируемые go tool: `testdata`, `vendor`, `_*`, `.*`.
- ✅ `go.mod`: module path = префикс импортов, не связан с именем папки; директива `go` = минимальная версия языка. ⬜ `go mod tidy`, `require`, `go.sum`.
- ✅ Модуль ≠ приложение: модуль — единица версионирования/зависимостей, приложение — пакет `main`; в модуле может быть несколько `main`.
- ⬜ Пакет = каталог; имя пакета vs путь импорта; почему не делать `utils`.
- ➕ `pkg/` — почему не используем на старте.
- ✅ Пакет из многих файлов, все с одним `package X`; в каталоге только `X` и `X_test`; вложенность каталогов ≠ иерархия пакетов.
- ✅ Файлы группируют по теме, не «один тип — один файл»; `types.go` для DTO нормально, как свалка — нет.
- ✅ `*_test.go` (компилируется только `go test`) vs `package X_test` (внешний, black-box, рвёт циклы) vs `package X` в тесте (внутренний, white-box).

## Данные и сериализация
- ✅ `encoding/json`: теги `json:"name"`, неизвестные поля игнорируются, отсутствующие = zero value; нет тега → match без регистра; unexported поля молча пропускаются; `UnmarshalJSON` как hook.
- ✅ Marshal = сериализация («выстроить»), Unmarshal/Decode = десериализация; `[]byte` API vs stream API (`Encoder/Decoder` над `io.Reader/Writer`). ✅ `io.Reader` = форма syscall `read`; `resp.Body` — интерфейс `io.ReadCloser`, указатель уже внутри; Decoder сам циклит `Read`; chunked прозрачен; `Close` возвращает соединение в пул. ⬜ `io.Writer`, `io.Copy`, `bufio`.
- 🟡 **ОБСУДИТЬ ОТДЕЛЬНО (запрос пользователя):** слайсы — заголовок (ptr, len, cap) 24 байта, два адреса (заголовок и данные), `byte`=`uint8`, cap как запас. ⬜ `append`, общие данные двух слайсов, nil vs пустой, `make`, `copy`; то же про string и map как «типы с указателем внутри».
- ✅ Отладчик GoLand/Delve: `{тип | значение}`, `&x` = адрес переменной, не данных; синтаксис `*(*"T")(addr)`. ✅ Map: `map[K]V`, ссылочный (указатель на хеш-таблицу), порядок случайный, zero value читается, `nil` map — panic на запись, `v, ok :=` форма, `make`/`delete`. ⬜ `time.Time`/`time.Duration`.

## Системный уровень (не Go, но нужно для понимания)
- ✅ Функция = байты инструкций в `.text` по фиксированному адресу; `CALL` (сохранить адрес возврата + jump), `RET`; метод = функция со скрытым первым аргументом receiver; одна копия кода на программу; function value = адрес кода (+ receiver).
- ✅ Мьютекс блокирует только тех, кто зовёт `Lock`, а не структуру/метод — защита на дисциплине.
- ✅ Файловый дескриптор = индекс в таблице открытых файлов процесса; сокет/файл/pipe — единый интерфейс read/write/close; `/proc/<pid>/fd`.
- ✅ Все компилируемые языки → один машинный код, ELF, SP, syscalls; Go добавляет runtime (GC, планировщик, дескрипторы); PHP/JS/Java — интерпретатор/VM.
- ✅ TCP-буфер ядра, `read` возвращает «сколько есть»; цепочка ядро → fd → `net.TCPConn` → HTTP body → `Decoder`.

## Архитектура в Go
- 🟡 Интерфейсы объявляет потребитель — принцип объяснён; ⬜ применить на репозиториях в домене.
- ✅ `context.Context` ≈ AbortController: `Background`, `WithTimeout` → `(ctx, cancel)`, `defer cancel()`, общий бюджет времени; отмена кооперативная — проверяет тот, кто хочет. ⬜ практика с shutdown и горутинами.
- ⬜ `errgroup` и lifecycle нескольких фоновых циклов.
- ✅ `error` = встроенный интерфейс `Error() string`; свой тип ошибки; `Unwrap()` для цепочки; `errors.Is` (сентинел) vs `errors.As` (по типу, `&ptr`); `%w`. ✅ sentinel-ошибки — экспортированные переменные пакета (`context.DeadlineExceeded`); `var x *T` как мишень для `errors.As`.
- ✅ `errors.As` смотрит на тип мишени, не значение; анонимный интерфейс `interface{ Unwrap() error }`; doc-комментарии = документация (`go doc`, pkg.go.dev), исходники всегда доступны.
- ✅ Enum через `type Kind string` + типизированные константы; `switch` без выражения.
- ✅ `defer`: при любом выходе из функции, LIFO, аргументы вычисляются сразу, привязан к функции (ловушка в цикле); почему после проверки `err` (nil resp). Игнор ошибки `Close` — норма. ⬜ замыкания `func(){...}()`.
- ⬜ `embed` для миграций.
- ✅ `httptest.NewServer` + `srv.Client()`; `http.HandlerFunc` как функция→интерфейс; замыкание захватывает `gotBody`. ⬜ разобрать HandlerFunc/интерфейсы и замыкания подробно.
- ✅ Нет assert в stdlib, `if got != want`; `testify` пишет в тот же `t`. Дженерики `[T any]` ≠ `testing.T`.
- ✅ Тесты: `TestXxx`, `Benchmark/Fuzz/Example`, нет аннотаций; `t.Run`, `t.Cleanup`, `t.Helper`, `TestMain`; `-run` regex. ✅ Табличные тесты = dataProvider без аннотаций; `%T`; `-v`, `-run`.

## База данных
- ✅ `COMMENT ON TABLE/COLUMN` в миграциях — обязательное требование, проверяется интеграционным тестом.
- ✅ Теория: `database/sql` (≈PDO) vs `pgx` напрямую; `pgxpool` один на процесс; `Exec` / `QueryRow().Scan` / `Query`+`rows.Next`; `$1` параметры; NULL ↔ указатели; `Begin`/`defer Rollback`/`Commit`; `pgx.Batch`; ctx отменяет запрос на сервере; рукописный SQL vs `sqlc` vs ORM; миграции goose+embed; тесты против настоящего Postgres.

## Типы и память
- ✅ Struct = непрерывная память с padding по выравниванию; порядок полей влияет на размер.
- ✅ Struct — значение (копируется), не ссылка; `var x T` = сама память, `&x` = указатель; аналог PHP array vs object. 🟡 когда `*T`, когда `T`.
- ✅ Имя переменной = адрес, известный компилятору (смещение в коде); указатель = адрес как данные в runtime. Передача по значению = копия всех байт (спецификация языка), `&x` = копия 8 байт адреса. Разобрано до уровня MOV.
- ✅ Два слоя структуры: экземпляр (байты как в C) и дескриптор типа (имена, смещения, теги — один на тип, читается `reflect`). Так работают `json`/`env`.
- ✅ `*T` в параметре = доступ к оригиналу (`*testing.T`); `&x`, `*p`, автоматическое разыменование `p.Field`.
- ✅ Стек один на горутину, вызов = кадр (frame), не новый стек; стек растёт копированием. Область видимости — компилятор, стек — время жизни; глобалы — сегмент данных, не куча.
- ✅ Стек (per-goroutine, локальные переменные, бесплатное освобождение) vs куча (GC; map-таблица, данные слайсов, утёкшие адреса); решает escape analysis, `-gcflags=-m`; `new(T)` и `&T{}` не гарантируют кучу.
- ✅ Метод = функция с receiver `func (id T) Name() R`; аналог `$this`; методы на любом именованном типе; `String()` для fmt. 🟡 value vs pointer receiver — на `Client`.
- ✅ Метаданные только на полях структур (теги); на типах/функциях аннотаций нет. Дескриптор есть у всех типов, для `int` минимален.
- ✅ Интерфейсы: набор сигнатур, implicit (структурная типизация как TS), значение = (дескриптор типа, указатель на данные), динамический вызов; потребитель объявляет интерфейс; маленькие интерфейсы; `any`. ⬜ type assertion `v.(T)`, type switch; nil-interface ловушка.
- 🟡 Встраивание вместо наследования.
- ✅ Видимость через регистр имени; struct tags (строка-метаданные, ключи `env:` — соглашение библиотеки, аналог PHP-атрибутов); raw string в обратных кавычках, интерполяции в Go нет; нулевые значения; `(T, error)` идиома.
- ✅ `/vN` в import path = мажорная версия, разные мажорные версии сосуществуют.
- ✅ Несколько возвращаемых значений; `:=` vs `=`; `error` — интерфейс, `nil` = нет ошибки; `Config{}` при ошибке.
- ✅ `log.Fatal` = print + `os.Exit(1)`, только в main; `panic` = баг, не ошибка окружения. ⬜ `defer`, схема `run() error`.
- ✅ `fmt` глаголы `%v %+v %#v %s`; `Stringer` как первый пример implicit interface. ⬜ интерфейсы подробно.
- ✅ Naming: аббревиатуры `URL/HTTP/ID`; `gofmt` как единственный стандарт форматирования.
- ✅ Конфиг: 12-factor env, `godotenv` в `config.Load`, `.env`/`.env.example`; порядок приоритета Load.
- ✅ `type Name Underlying`: именованные типы, зачем (методы, различимость `ServerID`/`PlayerID`), явное приведение. 🟡 enum через typed const. ⬜ `iota`.

## Из кода SearchRooms (2026-09-09) — на что посмотреть
- ⬜ `postJSON(..., reqBody any, out any)` — общий helper вместо дублирования; `any` как параметр, указатель в `out`.
- ⬜ `io.ReadAll(io.LimitReader(...))` — композиция Reader'ов, лимит на тело.
- ⬜ `json.RawMessage` — отложенный разбор поля неизвестной формы.
- ⬜ `*JoinQueue` (указатель на вложенную структуру): `nil` = поля не было в JSON.
- ⬜ `[]int{}` vs `nil` слайс → `[]` vs `null` в JSON.
- ⬜ `const op = "..."` внутри функции; `8 << 20` как 8 MiB.
- ⬜ Тесты: helper с `t.Helper()`, `t.Cleanup`, возврат нескольких значений (`gotPath *string`), `map[string]any` и `float64` для чисел из JSON, type assertion `.([]any)`.
- ⬜ Канал `chan struct{}` + `close()` как сигнал (тест таймаута); почему `<-r.Context().Done()` не сработал.
- ⬜ `%+v` на структуре в ошибках теста.

## Из кода token / config / probe (2026-09-09) — на что посмотреть
- ✅ `sync.Mutex`: замок Lock/Unlock, критическая секция, `defer Unlock`, поле `mu` над защищаемыми полями; data race показан детектором `go test -race` (тест `TestProvider_concurrentGet`). 🟡 `go func(){}()`, `sync.WaitGroup` — использованы в тесте, горутины подробно на планировщике.
- ✅ `now func() time.Time` — тип функции `func(params) results`, function value без скобок; аналог C-указателя на функцию, плюс замыкания; подмена часов в тестах.
- ✅ `Source` интерфейс: понято как «фабрика в зачатке» в `app.NewTokenProvider` + фейк в тестах; правило «интерфейс при второй реализации, фейк считается».
- ⬜ `atomic.Int32` в тесте — счётчик, безопасный между горутинами.
- ⬜ `var ErrNotAvailable = errors.New(...)` — свой sentinel; `errors.Is` на него.
- ⬜ Doc-комментарий пакета `// Package token ...` над `package`.
- ⬜ `envPrefix:"BOHEMIA_"` — вложенная структура конфига.
- ✅ `flag` пакет: `flag.String` → `*string`, `flag.Parse`, `-h` бесплатно; нет подкоманд.
- 🟡 Схема `main` → `run() error` → один `os.Exit`; `fmt.Fprintln(os.Stderr, ...)`.
- ✅ Функция с параметром `f(x T)` ≠ метод `(x T) f()`; методы только на типах своего пакета.
- ✅ Короткие имена пропорциональны области видимости — конвенция Go, пользователь принял.
- ⬜ `time.Unix(sec, 0).UTC().Format(time.RFC3339)`; `time.Time` разбирается из JSON сам (RFC 3339).
- ⬜ `%-40s` — выравнивание в Printf; `for i, r := range slice`, `for _, p := range`.

## На завтра (2026-09-10)
- Слайсы отдельно (см. раздел «Данные и сериализация»).
- Переименовать `internal/app/wire.go`? (`deps.go` / `build.go`).
- Живой запуск `go run ./cmd/probe -host 37.48.253.41:2001` после поднятия arma-reforger-hz.
- ✅ Сделано: Postgres в compose, миграции, каталог, планировщик.
- Далее: tracking (минутный опрос listPlayers), затем игроки/сессии; отдельно — деплой на удалённую машину (Dockerfile, compose).

## Из кода storage / observer (2026-09-10) — на что посмотреть
- ⬜ `//go:embed *.sql` + `embed.FS` — файлы вшиваются в бинарник на компиляции; директива над `var`.
- ⬜ `goose`: формат `-- +goose Up / Down`, таблица `goose_db_version`, идемпотентность.
- ⬜ `pgxpool.Pool` — пул соединений; `stdlib.OpenDBFromPool` — мост pgx → `database/sql` для goose.
- ⬜ `pool.QueryRow(ctx, sql).Scan(&x)` — чтение одной строки; плейсхолдеры `$1` (в следующем шаге).
- ⬜ Интеграционный тест с `t.Skip` по отсутствию `DATABASE_URL`.
- ⬜ `log/slog`: `SetDefault`, JSON-handler, пары ключ-значение `slog.Info("msg", "k", v)`.
- ⬜ `bigint GENERATED ALWAYS AS IDENTITY`, `timestamptz`, partial index `WHERE tracking_enabled` (SQL, не Go).

## Из кода catalog / observation / CatalogRepo (2026-09-10) — на что посмотреть
- ⬜ Callback-пагинация `visit func(page) error` — обработка потока страниц без накопления 35 МБ в памяти.
- ⬜ Интерфейсы `Source`, `TokenProvider`, `Repository` объявлены в `catalog` (потребитель), фейки в тесте — три интерфейса, ноль моков-библиотек.
- ⬜ `var _ catalog.Repository = (*CatalogRepo)(nil)` — проверка реализации интерфейса на этапе компиляции.
- ⬜ Транзакция pgx: `Begin` / `defer tx.Rollback(ctx)` / `Commit` — Rollback после Commit безвреден.
- ⬜ `errors.Is(err, pgx.ErrNoRows)` — «не найдено» как sentinel, не ошибка.
- ⬜ `pgx.Batch` — несколько запросов одним round-trip.
- ⬜ `*int`, `*time.Time` как nullable для SQL; `unixOrNil`; `&q.Size` — адрес поля структуры.
- ⬜ `for i := range rooms { room := &rooms[i] }` — указатель на элемент слайса вместо копии.
- ⬜ Типизированные константы `PollType`, `Status` в `observation`; `string(run.Status)` при передаче в SQL.
- ⬜ `ON CONFLICT ... DO UPDATE SET x = EXCLUDED.x` — upsert (SQL).
- ⬜ Интеграционные тесты делят dev-БД и оставляют строки (AUTH_ERROR в poll_run — из теста). TODO: отдельная `DATABASE_URL_TEST`.

## Из кода планировщика (2026-09-10, cmd/observer, catalog/loop.go) — на что посмотреть
- ⬜ `go func() {...}()` — запуск горутины; `errgroup.WithContext` — группа горутин, первая ошибка отменяет ctx остальным, `g.Wait()`.
- ⬜ `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` — Ctrl+C / `docker stop` превращаются в отмену контекста.
- ⬜ `select { case <-timer.C: ... case <-ctx.Done(): ... }` — ожидание первого из двух событий; почему `time.Sleep` не годится.
- ⬜ Каналы: `<-ch` чтение, `chan error` с буфером 1 в тесте, `ctx.Done()` тоже канал.
- ⬜ Цикл, который возвращается только по `ctx.Err()`; проверка `ctx.Err() != nil` после операции, чтобы отличить отмену от ошибки.
- ⬜ Гонка в собственном тесте (`fakeRepo.saved`) — поймана `-race`, лечится мьютексом в фейке. Урок: любые данные, которые трогают две горутины, нужно защищать.
- ⬜ `errors.Is(err, context.Canceled)` — штатная остановка не ошибка.

## Из кода MVP-коллектора (2026-09-10, presence / tracking / httpapi / deploy) — на что посмотреть, когда будет время
- ⬜ `presence`: машина состояний как чистая функция над `Tx`-интерфейсом; `sessionOps` — маленький интерфейс, чтобы один алгоритм обслуживал присутствие и очередь.
- ⬜ In-memory фейк `memStore`, реализующий сразу `Store` и `Tx` — тесты бизнес-логики без Postgres.
- ⬜ `errgroup.SetLimit(n)` — ограничение параллелизма вместо ручного семафора; `g.Go` в цикле `for _, srv := range` (Go ≥1.22: переменная цикла своя на итерацию).
- ⬜ `FOR UPDATE` в выборке открытых сессий — блокировка строк в транзакции.
- ⬜ `RETURNING id, (xmax = 0)` — отличить INSERT от UPDATE в upsert (Postgres-трюк).
- ⬜ `time.NewTicker` в `RunLoop` трекера vs таймер в цикле скана.
- ⬜ `http.ServeMux` с методом в паттерне `"GET /health"`; таймауты `http.Server`; `Shutdown` по ctx.
- ⬜ `LEFT JOIN LATERAL` для «последний poll на сервер» в статусе (SQL).
- ⬜ Dockerfile: multi-stage, `CGO_ENABLED=0`, distroless, `--mount=type=cache`.
- ⬜ Семафор на канале `chan struct{}` с буфером N + `sync.WaitGroup` — ручное ограничение параллелизма (в `tracking.PollAll`, вместо `errgroup.SetLimit`).

## 2026-09-11 — на что посмотреть
- ⬜ `crypto/sha1` + `sort.Strings` для хеша набора (`modSetHash`); хеш как дешёвая проверка «изменилось ли».
- ⬜ Именованные возвращаемые значения `(serverID int64, modHash string, created bool, err error)` в `resolveOrCreateServer`.
- ⬜ `text[]` ↔ `[]string` в pgx без конвертации; `RETURNING` в UPDATE.
- ⬜ CTE-цепочка с несколькими UPDATE … RETURNING в одном запросе (`MergeDuplicateRooms`) — SQL.
