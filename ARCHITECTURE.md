# Архитектура сервиса лояльности «Гофермарт»

Документ описывает архитектуру решения для корректирующего ревью: HTTP-хендлеры, канальную обработку начислений и схему базы данных.

---

## HTTP-хендлеры

### `POST /api/user/register` — регистрация пользователя

1. Чтение JSON-тела запроса с полями `login` и `password`.
2. Проверка, что логин и пароль непусты. Если пусты — ответ с кодом 400 (неверный формат запроса).
3. Вызов `service.RegisterUser`: пароль хешируется через bcrypt (cost 12), в БД сохраняется только хеш.
4. Если логин уже занят — сервис возвращает `ErrLoginTaken`, хендлер отвечает кодом 409 (конфликт).
5. При успехе — генерация JWT (HS256, TTL 24 часа), установка cookie `gophermart_auth` с параметрами `HttpOnly: true`, `SameSite: Strict`, `Path: /`.
6. Ответ с кодом 200 (успешная регистрация).

### `POST /api/user/login` — аутентификация пользователя

1. Чтение JSON-тела запроса с полями `login` и `password`.
2. Проверка, что логин и пароль непусты. Если пусты — ответ с кодом 400 (неверный формат запроса).
3. Вызов `service.AuthenticateUser`: поиск пользователя по логину, сравнение пароля через `bcrypt.CompareHashAndPassword`.
4. Если пользователь не найден или пароль не совпадает — сервис возвращает `ErrInvalidCredentials`, хендлер отвечает кодом 401 (неавторизован).
5. При успехе — генерация JWT и установка cookie, аналогично регистрации.
6. Ответ с кодом 200 (успешный вход).

**Особенности.** Пароли в БД не хранятся — только bcrypt-хеши. Сервисный слой транслирует ошибки хранилища (`repository.ErrLoginTaken`, `repository.ErrUserNotFound`) в собственные sentinel-ошибки (`service.ErrLoginTaken`, `service.ErrInvalidCredentials`), поэтому хендлер работает только с типами из пакета `service` и не импортирует `repository`.

### `POST /api/user/orders` — загрузка номера заказа

1. Извлечение ID пользователя из контекста запроса (через middleware `RequireAuth`).
2. Чтение тела запроса как `text/plain` через `io.ReadAll` (не JSON).
3. Проверка, что номер заказа непустой. Если пустой — ответ с кодом 400 (неверный формат запроса).
4. Проверка номера по алгоритму Луна. Если не проходит — ответ с кодом 422 (неверный номер заказа).
5. Вызов `service.UploadOrder`: попытка INSERT в таблицу `orders`.
6. Если `UploadOrder` возвращает `ErrOrderAlreadyExists` — второй запрос `GetOrderByNumber` для определения владельца:
   - владелец — тот же пользователь → ответ с кодом 200 (заказ уже загружен этим пользователем);
   - владелец — другой пользователь → ответ с кодом 409 (заказ загружен другим пользователем).
7. Если `UploadOrder` возвращает `nil` — заказ новый, постановка в очередь `accrualQueue`, ответ с кодом 202 (заказ принят в обработку).

**Узкое место.** При дубликате выполняется два запроса к БД: неуспешный INSERT (упирается в unique constraint на колонке `number`) и SELECT для определения владельца. Альтернатива — `INSERT ... ON CONFLICT DO NOTHING RETURNING`, но текущий подход выбран в пользу читаемости.

### `GET /api/user/orders` — список заказов пользователя

1. Извлечение ID пользователя из контекста запроса.
2. Вызов `service.ListUserOrders`: SELECT из БД с сортировкой `ORDER BY uploaded_at DESC` (от новых к старым).
3. Если список пуст — ответ с кодом 204 (нет данных).
4. Если список не пуст — формирование JSON-массива. Поле `accrual` включается в ответ только для заказов со статусом `PROCESSED` и положительным значением начисления (через `*decimal.Decimal` с `omitempty`). Для статусов `NEW`, `PROCESSING`, `INVALID` поле `accrual` отсутствует.
5. Ответ с кодом 200 и JSON-телом, заголовок `Content-Type: application/json`.

**Особенности.** Сервис возвращает `OrderDTO` (не `repository.Order`), что избавляет хендлер от импорта пакета `repository`.

### `GET /api/user/balance` — текущий баланс

1. Извлечение ID пользователя из контекста запроса.
2. Вызов `service.GetBalance`: один SELECT из таблицы `users` — колонки `balance` и `withdrawn`.
3. Ответ с кодом 200 и JSON `{current, withdrawn}`. Баллы — `NUMERIC(12,2)` в БД, `decimal.Decimal` в Go (не `float64`, чтобы избежать потери точности).

### `POST /api/user/balance/withdraw` — списание баллов

1. Извлечение ID пользователя из контекста запроса.
2. Чтение JSON-тела с полями `order` (номер заказа) и `sum` (сумма списания).
3. Проверка, что номер заказа непустой. Если пустой — ответ с кодом 400.
4. Проверка номера по алгоритму Луна. Если не проходит — ответ с кодом 422 (неверный номер заказа).
5. Проверка, что `sum > 0`. Если нет — ответ с кодом 400 (сумма должна быть положительной).
6. Вызов `service.WithdrawPoints`: одна транзакция из четырёх шагов (см. раздел «Схема БД — транзакция списания»).
7. При ошибке `ErrInsufficientFunds` — ответ с кодом 402 (недостаточно средств).
8. При ошибке `ErrOrderAlreadyExists` — ответ с кодом 422 (заказ уже зарегистрирован).
9. При успехе — постановка заказа в очередь `accrualQueue`, ответ с кодом 200.

**Архитектурное решение.** По спецификации `withdraw` не требует взаимодействия с accrual-системой. В текущей реализации через `withdraw` создаётся полноценный заказ, который уходит в accrual-очередь для начисления баллов. Пока решено оставить так — это позволяет списанные заказы проходить полный цикл начисления.

### `GET /api/user/withdrawals` — список списаний

1. Извлечение ID пользователя из контекста запроса.
2. Вызов `service.ListWithdrawals`: SELECT из БД с сортировкой `ORDER BY processed_at DESC` (от новых к старым).
3. Если список пуст — ответ с кодом 204 (нет данных).
4. Если список не пуст — формирование JSON-массива, ответ с кодом 200.

**Особенности.** Переменная цикла названа `wl` (а не `w`), чтобы не затенять параметр `ResponseWriter` — это предупреждение `govet` о shadowing.

### `GET /ping` — проверка доступности БД

1. Вызов `service.Ping`: `SELECT 1` через `pool.Ping`.
2. Ответ с кодом 200 при успехе, кодом 500 при недоступности.
3. Эндпоинт не требует аутентификации.

---

## Канальная архитектура

### Обзор

Два буферизированных канала и два воркера образуют конвейер асинхронной обработки:

1. `accrualQueue` (буфер 2048) — канал номеров заказов для опроса accrual-системы.
2. Accrual Worker — горутина, читающая из `accrualQueue` и опрашивающая accrual-систему.
3. `dbUpdateQueue` (буфер 1024) — канал обновлений статусов и начислений для записи в БД.
4. Batch Processor — горутина, накапливающая обновления и пакетно записывающая их в БД.

### `accrualQueue` — очередь запросов к accrual-системе

Канал `chan string` размером 2048. Источники:

- `uploadOrder` — новый заказ после загрузки пользователем;
- `withdraw` — новый заказ после списания баллов;
- `EnqueuePendingOrders` — startup-sweep при рестарте (см. ниже);
- `requeueOrder` — переотправка нефинальных заказов через задержку.

Функция `enqueueOrder` использует `select` с тремя ветками:

1. `<-s.closed` — сервис останавливается, отправка отменяется;
2. `s.accrualQueue <- orderNumber` — успешная отправка;
3. `default` — очередь переполнена, логируется предупреждение; заказ остаётся в БД со статусом `NEW` и будет обработан при рестарте.

### Accrual Worker

Воркер читает номера заказов из `accrualQueue` и опрашивает accrual-систему через `AccrualClient.GetOrderAccrual`. Для каждого заказа вызывается `processOneOrder`, который маршрутизирует ответ:

1. **Ответ с кодом 200, статус INVALID или PROCESSED** — отправка `OrderUpdate` в `dbUpdateQueue`, заказ не переотправляется (достигнут финальный статус).
2. **Ответ с кодом 200, статус REGISTERED или PROCESSING** — отправка `OrderUpdate` в `dbUpdateQueue` (обновит статус на `PROCESSING`), заказ переотправляется в `accrualQueue` через `AccrualRetryDelay` (3 секунды) через `requeueOrder`.
3. **Ответ с кодом 204 (заказ не зарегистрирован в accrual-системе)** — заказ переотправляется через `AccrualRetryDelay`.
4. **Ответ с кодом 429 от accrual-системы (превышен лимит запросов)** — заказ переотправляется через `AccrualRetryDelay`, а `processOneOrder` возвращает задержку из заголовка `Retry-After` как `time.Duration` (см. ниже замечание об обработке 429).
5. **Ошибка сети** — заказ переотправляется через `AccrualRetryDelay`.

Между запросами выдерживается `AccrualPollInterval` — 100 мс. Задержка реализована через `select` на `time.After` с проверкой `closed` и `ctx.Done()`.

**Обработка 429.** При получении ответа с кодом 429 от accrual-системы воркер **полностью останавливается** на срок, указанный в `Retry-After`. В течение этого времени никакие другие заказы из очереди не отправляются в accrual-систему. Это предотвращает бан со стороны accrual-системы. Логика выбора задержки:

```go
delay := s.processOneOrder(ctx, orderNumber)
wait := s.config.AccrualPollInterval
if delay > 0 {
    wait = delay // при 429 — полный останов на Retry-After
}
select {
case <-time.After(wait):
case <-s.closed:
case <-ctx.Done():
}
```

Если бы воркер при 429 лишь переотправлял конкретный заказ с задержкой, но продолжал опрашивать accrual-систему по остальным заказам из очереди, это привело бы к лавине запросов и блокировке со стороны accrual-системы.

**`requeueOrder`.** Использует `time.AfterFunc(delay, func() { s.enqueueOrder(orderNumber) })` — таймер из стандартной библиотеки, который запускает callback в отдельной горутине по истечении `delay`. При срабатывании `enqueueOrder` проверяет сигнальный канал `closed` и безопасно отменяет отправку, если сервис останавливается. Это эффективнее ручной горутины с `time.After` и `select`, поскольку используется внутренний таймер-колесо рантайма.

### `dbUpdateQueue` — очередь обновлений БД

Канал `chan repository.OrderUpdate` размером 1024. Источник — только accrual-воркер. Воркер закрывает этот канал при выходе (`close(s.dbUpdateQueue)`), что каскадно останавливает batch processor.

### Batch Processor

Накапливает обновления в слайс и сбрасывает в БД двумя триггерами:

1. Размер батча достиг `DBUpdateBatchSize` (512 элементов);
2. Сработал `time.Ticker` каждые `DBUpdateFlushTimeout` (500 мс).

`flush` копирует батч, сбрасывает слайс и запускает запись в отдельной горутине через `sync.WaitGroup`. Запись использует **свежий контекст** с таймаутом 10 секунд (`context.WithTimeout(context.Background(), ...)`), а не отменённый `ctx` — это гарантирует, что финальный батч при остановке не потеряется.

`BatchUpdateOrders` выполняет все обновления в одной транзакции: для каждого заказа — `UPDATE orders SET status, accrual`, а для `PROCESSED` с положительным accrual — дополнительный `UPDATE users SET balance = balance + accrual WHERE id = (SELECT user_id FROM orders WHERE number = ...)`.

**Узкое место.** Каждый элемент батча — два отдельных `EXEC` (UPDATE заказа + UPDATE баланса). Оптимизация возможна через multi-row UPDATE с `UNNEST`, но пока решено оставить текущий подход ради читаемости.

### Graceful shutdown

Цепочка остановки:

1. `StopAccrualProcessor()` закрывает сигнальный канал `closed` через `sync.Once`.
2. `enqueueOrder` и callback-функции `time.AfterFunc` в `requeueOrder` видят `closed` — новые отправки отменяются.
3. `runAccrualWorker` выходит по `case <-s.closed`, закрывает `dbUpdateQueue`.
4. `runBatchProcessor` выходит по `case !ok` (канал закрыт), делает финальный `flush` и ждёт `storageWg`.
5. `main.go` ждёт `backgroundWg`.

**Канал `accrualQueue` намеренно не закрывается** при остановке. Это предотвращает panic в callback-функциях `time.AfterFunc` из `requeueOrder`, которые могут сработать после `StopAccrualProcessor` и попытаться записать в закрытый канал. Вместо этого воркер выходит по сигналу `closed` и закрывает `dbUpdateQueue`, что каскадно останавливает batch processor.

### Startup-sweep

При запуске `EnqueuePendingOrders` выбирает из БД все заказы в статусах `NEW` и `PROCESSING` (`ListPendingOrderNumbers`) и ставит их в `accrualQueue`. Последовательность:

1. SELECT номеров заказов со статусами `NEW` и `PROCESSING` из таблицы `orders`.
2. Для каждого номера — вызов `enqueueOrder` (постановка в `accrualQueue`).
3. Логирование количества поставленных в очередь заказов.

Это гарантирует, что заказы, не дошедшие до accrual при предыдущей остановке, не потеряются.

---

## Схема базы данных

Три таблицы, одна миграция (`0001_init.up.sql`).

### Таблица `users`

| Колонка | Тип | Назначение |
|---|---|---|
| `id` | `BIGSERIAL PRIMARY KEY` | идентификатор |
| `login` | `TEXT NOT NULL UNIQUE` | логин |
| `password_hash` | `TEXT NOT NULL` | bcrypt-хеш пароля |
| `balance` | `NUMERIC(12,2) DEFAULT 0` | текущий баланс баллов |
| `withdrawn` | `NUMERIC(12,2) DEFAULT 0` | суммарное списание |
| `created_at` | `TIMESTAMPTZ DEFAULT now()` | время регистрации |

Баланс и списания хранятся прямо в таблице пользователей — отдельной таблицы транзакций нет. Это упрощает чтение (`GET /balance` — один `SELECT`), но усложняет аудит (нет истории изменений баланса).

### Таблица `orders`

| Колонка | Тип | Назначение |
|---|---|---|
| `id` | `BIGSERIAL PRIMARY KEY` | идентификатор |
| `number` | `TEXT NOT NULL UNIQUE` | номер заказа (глобально уникален) |
| `user_id` | `BIGINT FK → users(id)` | владелец |
| `status` | `TEXT DEFAULT 'NEW'` | внутренний статус: NEW, PROCESSING, INVALID, PROCESSED |
| `accrual` | `NUMERIC(12,2) DEFAULT 0` | начисленные баллы |
| `uploaded_at` | `TIMESTAMPTZ DEFAULT now()` | время загрузки |

Индексы: `idx_orders_user_id` (для `ListUserOrders`), `idx_orders_status` (для `ListPendingOrderNumbers`).

Колонка `number` уникальна глобально — один и тот же номер заказа не может быть загружен двумя разными пользователями. На уровне БД это unique constraint, на уровне хендлера — ответ с кодом 409 (заказ загружен другим пользователем).

### Таблица `withdrawals`

| Колонка | Тип | Назначение |
|---|---|---|
| `id` | `BIGSERIAL PRIMARY KEY` | идентификатор |
| `user_id` | `BIGINT FK → users(id)` | кто списал |
| `order_number` | `TEXT NOT NULL` | номер заказа |
| `sum` | `NUMERIC(12,2) NOT NULL` | сумма списания |
| `processed_at` | `TIMESTAMPTZ DEFAULT now()` | время списания |

Индекс: `idx_withdrawals_user_id`. Внешний ключ `order_number` → `orders.number` не наложен, хотя заказ создаётся в той же транзакции.

### Транзакция списания `WithdrawPoints`

Четыре шага в одной транзакции:

1. `pg_advisory_xact_lock($userID)` — сериализация операций по пользователю. Блокировка транзакционная (снимается при commit/rollback), не требует явного освобождения.
2. `UPDATE users SET balance = balance - sum, withdrawn = withdrawn + sum WHERE id = $userID AND balance >= sum RETURNING balance` — атомарная проверка и списание. Если `ErrNoRows` → `ErrInsufficientFunds` (ответ с кодом 402, недостаточно средств).
3. `INSERT INTO orders (number, user_id, status) VALUES ($1, $2, 'NEW')` — создание заказа. Unique constraint violation → `ErrOrderAlreadyExists` (транзакция откатывается, ответ с кодом 422).
4. `INSERT INTO withdrawals (user_id, order_number, sum) VALUES ($1, $2, $3)` — фиксация факта списания.

**Почему `pg_advisory_xact_lock`, а не `SELECT FOR UPDATE`.** Advisory lock не блокирует строку — он блокирует «замок» по ID пользователя. Меньше overhead, нет необходимости держать строку заблокированной до конца транзакции, нет риска deadlock при неаккуратном порядке блокировок.

---

## Слоистая архитектура

```
handler → service → repository → pgx → PostgreSQL
                ↘ accrual.Client → accrual-система
```

1. **handler** — HTTP-хендлеры на chi. Не импортирует `repository` — сервис возвращает `OrderDTO` и `WithdrawalDTO`. Проверяет только sentinel-ошибки из `service`.
2. **service** — бизнес-логика, bcrypt, каналы, воркеры. Транслирует ошибки `repository` в собственные sentinel-ошибки. Обращается к accrual-системе через интерфейс `AccrualClient`, что позволяет подменять моком в тестах.
3. **repository** — хранилище на pgx. Использует приватный интерфейс `dbtx` (реализуется `*pgxpool.Pool` и `pgx.Tx`) — для unit-тестов без живой БД.
4. **accrual.Client** — HTTP-клиент с таймаутом 5 секунд, парсинг `Retry-After` при 429.
