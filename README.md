# Накопительная система лояльности «Гофермарт»

Сервис лояльности для начисления баллов за покупки и списания их в счёт новых заказов. Работа выполнена согласно [техническому заданию](SPECIFICATION.md), архитектура решения описана в [ARCHITECTURE.md](ARCHITECTURE.md).

## Содержание

- [Описание](#описание)
- [Требования к окружению](#требования-к-окружению)
- [Сборка и запуск](#сборка-и-запуск)
  - [Команды Make](#команды-make)
  - [Сборка вручную](#сборка-вручную)
  - [Запуск accrual-эмулятора](#запуск-accrual-эмулятора)
  - [Запуск сервиса gophermart](#запуск-сервиса-gophermart)
- [Конфигурация](#конфигурация)
  - [Флаги командной строки](#флаги-командной-строки)
  - [Переменные окружения](#переменные-окружения)
  - [Подключение к PostgreSQL](#подключение-к-postgresql)
  - [Параметры, не задаваемые извне](#параметры-не-задаваемые-извне)
- [Миграции базы данных](#миграции-базы-данных)
- [Тестирование](#тестирование)
  - [Команды Make для тестов](#команды-make-для-тестов)
  - [Ручной запуск тестов](#ручной-запуск-тестов)
  - [Структура тестов](#структура-тестов)
  - [Мокирование БД (pgxmock)](#мокирование-бд-pgxmock)
  - [Моки интерфейсов (mockery)](#моки-интерфейсов-mockery)
  - [Подсчёт покрытия](#подсчёт-покрытия)
- [Структура проекта](#структура-проекта)

## Описание

«Гофермарт» — HTTP-сервис, реализующий накопительную систему лояльности. Пользователи регистрируются, загружают номера заказов, система опрашивает внешнюю accrual-систему для расчёта начислений и ведёт баланс баллов. Баллы можно списывать в счёт новых заказов.

## Требования к окружению

- **ОС:** Linux, WSL
- **Go:** 1.26+
- **PostgreSQL:** 14+
- **mockery:** v3 (для регенерации моков; `go install github.com/vektra/mockery/v3@latest`)

## Сборка и запуск

### Команды Make

В корне проекта находится `Makefile` со следующими целями:

| Команда | Описание |
|---|---|
| `make build` | Собрать бинарник в `./bin/gophermart` |
| `make run` | Собрать и запустить (нужны env-переменные или флаги) |
| `make test` | Запустить все тесты |
| `make test-v` | Запустить все тесты с verbose-выводом |
| `make test-pkg PKG=./internal/handler` | Запустить тесты конкретного пакета |
| `make cover` | Итоговый процент покрытия (без моков, `cmd/`, `migrations/`) |
| `make cover-html` | HTML-отчёт покрытия (без моков, `cmd/`, `migrations/`) |
| `make mocks` | Сгенерировать моки через mockery |
| `make tidy` | `go mod tidy` |
| `make fmt` | Отформатировать код |
| `make vet` | Статический анализ |
| `make lint` | `fmt` + `vet` |
| `make clean` | Удалить бинарник и файлы покрытия |
| `make all` | `lint` + `build` + `test` |

Быстрый старт — проверка кода, сборка и прогон тестов одной командой:

```bash
make all
```

### Сборка вручную

Если `make` недоступен, бинарник собирается командой:

```bash
go build -o ./bin/gophermart ./cmd/gophermart
```

### Запуск accrual-эмулятора

Эмулятор системы расчёта вознаграждений имеет встроенное in-memory хранилище, поэтому для запуска достаточно указать адрес:

```bash
./cmd/accrual/accrual_linux_amd64 -a ":8088"
```

Здесь `":8088"` — адрес и порт, на которых эмулятор будет принимать запросы.

### Запуск сервиса gophermart

```bash
./bin/gophermart -a :8080 -l debug -d "postgres://gophermart:SECRET@localhost:5432/gophermart?sslmode=disable" -r "http://localhost:8088"
```

## Конфигурация

Конфигурация загружается из двух источников. **Приоритет (от низшего к высшему):**

1. Жёстко заданные значения по умолчанию
2. Переменные окружения — перекрывают значения по умолчанию
3. Флаги командной строки — перекрывают переменные окружения

### Флаги командной строки

| Флаг | Описание | Значение по умолчанию |
|---|---|---|
| `-a` | Адрес и порт запуска HTTP-сервера | `:8080` (или `RUN_ADDRESS` из env) |
| `-d` | Строка подключения к PostgreSQL | (нет; или `DATABASE_URI` из env) |
| `-r` | Адрес accrual-системы | (нет; или `ACCRUAL_SYSTEM_ADDRESS` из env) |
| `-l` | Уровень логирования (`debug`, `info`, `warn`, `error`) | `info` (или `LOG_LEVEL` из env) |

### Переменные окружения

| Переменная | Описание | Значение по умолчанию |
|---|---|---|
| `RUN_ADDRESS` | Адрес и порт запуска HTTP-сервера | `:8080` |
| `DATABASE_URI` | Строка подключения к PostgreSQL | (нет, обязательна) |
| `ACCRUAL_SYSTEM_ADDRESS` | Адрес accrual-системы | (нет, обязательна) |
| `LOG_LEVEL` | Уровень логирования | `info` |
| `SIGNING_KEY` | Ключ подписи JWT-токенов | `gophermart-signing-key` |

Обязательные параметры — `DATABASE_URI` и `ACCRUAL_SYSTEM_ADDRESS`. При их отсутствии сервис выведет ошибку и завершится.

### Подключение к PostgreSQL

Строка подключения передаётся в формате DSN, например:

```
postgres://gophermart:SECRET@localhost:5432/gophermart?sslmode=disable&pool_max_conns=10&pool_max_conn_lifetime=30m&pool_min_conns=2&pool_max_conn_idle_time=5m
```

Поддерживаемые параметры pgxpool:

| Параметр | Описание | Пример |
|---|---|---|
| `sslmode` | Режим SSL (`disable`, `require`, `verify-full`) | `sslmode=disable` |
| `pool_max_conns` | Максимальное число соединений в пуле | `pool_max_conns=10` |
| `pool_min_conns` | Минимальное число соединений в пуле | `pool_min_conns=2` |
| `pool_max_conn_lifetime` | Время жизни соединения | `pool_max_conn_lifetime=30m` |
| `pool_max_conn_idle_time` | Время простоя соединения | `pool_max_conn_idle_time=5m` |

### Параметры, не задаваемые извне

Следующие параметры заданы в коде и не конфигурируются через env или флаги:

| Параметр | Значение | Описание |
|---|---|---|
| `CookieAuthName` | `gophermart_auth` | Имя cookie для хранения JWT |
| `CookieAuthTTL` | 24 часа | Время жизни cookie аутентификации |
| `ShutdownTimeout` | 10 секунд | Таймаут graceful shutdown HTTP-сервера |
| `SigningKey` | `gophermart-signing-key` | Ключ JWT (если `SIGNING_KEY` не задан в env) |

## Миграции базы данных

Миграции SQL встроены в бинарник (`//go:embed *.sql`) и применяются автоматически при запуске сервиса — отдельной команды не требуется. Файлы миграций находятся в каталоге `migrations/`.

Схема базы данных включает три таблицы:

- **users** — пользователи, баланс и сумма списаний
- **orders** — заказы со статусом (ENUM: `NEW`, `PROCESSING`, `INVALID`, `PROCESSED`) и начисленными баллами
- **withdrawals** — списания баллов в счёт заказов

## Тестирование

Проект покрыт юнит-тестами — 195 тестов в 9 пакетах. Целевой уровень покрытия — не менее 60%. Тесты используют стандартный пакет `testing` совместно с библиотекой `testify` (`assert`, `require`, `suite`) и подход Table Driven Test для простых сценариев.

### Команды Make для тестов

```bash
make test                                        # все тесты
make test-v                                      # все тесты с verbose-выводом
make test-pkg PKG=./internal/handler              # тесты одного пакета
make cover                                       # итоговый процент покрытия
make cover-html                                  # HTML-отчёт покрытия
```

### Ручной запуск тестов

Перед первым запуском тестов:

```bash
go mod tidy
```

Запуск всех тестов с покрытием:

```bash
go test -cover ./...
```

Запуск тестов отдельного пакета:

```bash
go test -v -cover ./internal/repository
```

### Структура тестов

| Пакет | Тестов | Инструмент мокирования | Подход |
|---|---|---|---|
| `internal/repository` | 6 | `pgxmock/v4` — мок pgx-пула | Table-driven (suite), транзакции мокируются через `ExpectBegin`/`ExpectCommit`/`ExpectRollback` |
| `internal/luhn` | 4 | не требуется (чистая функция) | Table-driven |
| `internal/auth` | 14 | не требуется (чистая логика JWT) | testify assert/require |
| `internal/config` | 11 | не требуется (env-манипуляция) | testify assert |
| `internal/logger` | 9 | `httptest` | testify assert |
| `internal/service` | 49 | mockery-моки интерфейсов `Storage`, `AccrualClient` | suite + моки |
| `internal/handler` | 69 | mockery-моки `GopherMartService` + `httptest` | suite + моки |
| `internal/accrual` | 33 | `httptest.Server` (мок HTTP) | testify assert/require |
| **Итого** | **195** | | |

### Мокирование БД (pgxmock)

Для тестов слоя хранилища используется [pgxmock v4](https://github.com/pashagolub/pgxmock) — мок pgx, не требующий реального подключения к PostgreSQL. Каждый тест создаёт собственный экземпляр мока через `pgxmock.NewPool()` и проверяет, что все ожидания были удовлетворены, через `mock.ExpectationsWereMet()`.

### Моки интерфейсов (mockery)

Для мокирования интерфейсов сервисного и хендлерного слоёв используется [mockery v3](https://vektra.github.io/mockery/). Конфигурация генерации хранится в файле `.mockery.yaml` в корне проекта. Моки генерируются в каталог `mocks/` рядом с тестируемым пакетом.

Установка mockery v3:

```bash
go install github.com/vektra/mockery/v3@latest
```

Генерация всех моков одной командой из корня проекта:

```bash
mockery
```

или через Make:

```bash
make mocks
```

Mockery v3 автоматически найдёт `.mockery.yaml` и сгенерирует моки для всех указанных интерфейсов. Сгенерированные файлы:

| Файл | Интерфейс | Пакет |
|---|---|---|
| `internal/service/mocks/mock_Storage.go` | `Storage` | `mocks` |
| `internal/service/mocks/mock_AccrualClient.go` | `AccrualClient` | `mocks` |
| `internal/handler/mocks/mock_GopherMartService.go` | `GopherMartService` | `mocks` |

Моки используют стиль `EXPECT()`:

```go
storage.EXPECT().CreateUser(mock.Anything, "alice", mock.AnythingOfType("string")).
    Return(int64(42), nil)
```

При изменении интерфейсов удалите старые моки и перегенерируйте:

```bash
rm internal/service/mocks/mock_*.go internal/handler/mocks/mock_*.go
mockery
```

### Подсчёт покрытия

Профиль покрытия собирается командой `go test -coverprofile`. Сгенерированные моки, точка входа `cmd/gophermart/main.go` и пакет `migrations/` исключаются из подсчёта, так как не являются тестируемым кодом:

```bash
go test -coverprofile=coverage.out ./...
grep -v -e '/mocks/' -e '/cmd/gophermart/' -e '/migrations/' coverage.out > coverage_filtered.out
go tool cover -func=coverage_filtered.out | tail -1
```

То же через Make:

```bash
make cover        # итоговый процент в терминале
make cover-html   # HTML-отчёт в браузере
```

## Структура проекта

```
gophermart/
├── cmd/
│   ├── gophermart/          # Точка входа сервиса
│   │   └── main.go
│   └── accrual/             # Эмулятор accrual-системы (бинарник)
├── internal/
│   ├── accrual/             # HTTP-клиент accrual-системы (ретраи 5xx, 429)
│   ├── auth/                # Генерация и валидация JWT
│   ├── config/              # Загрузка конфигурации (env + флаги)
│   ├── handler/            # HTTP-хендлеры и middleware
│   ├── logger/             # Инициализация zap-логгера
│   ├── luhn/               # Проверка номера заказа по алгоритму Луна
│   ├── repository/         # Слой хранилища (PostgreSQL через pgx)
│   └── service/            # Бизнес-логика, worker pool, batch processor
├── migrations/             # SQL-миграции (embed в бинарник)
├── ARCHITECTURE.md         # Описание архитектуры
├── SPECIFICATION.md        # Техническое задание
├── Makefile                # Команды сборки, тестирования, покрытия
└── README.md               # Этот файл
```
