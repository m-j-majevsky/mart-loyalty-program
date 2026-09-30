package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/m-j-majevsky/gophermart/internal/config"
	"github.com/m-j-majevsky/gophermart/internal/logger"
	"github.com/m-j-majevsky/gophermart/internal/luhn"
	"github.com/m-j-majevsky/gophermart/internal/service"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// jsonNumber — обёртка над decimal.Decimal, сериализуется в JSON как число,
// а не как строка. Стандартный decimal.MarshalJSON отдаёт значение в кавычках,
// что не удовлетворяет спеке сервиса.
type jsonNumber decimal.Decimal

// MarshalJSON сериализует значение как число без кавычек.
func (n jsonNumber) MarshalJSON() ([]byte, error) {
	d := decimal.Decimal(n)
	s := d.String()
	// Убираем trailing zeros после десятичной точки,
	// чтобы NUMERIC(12,2) → "500.00" сериализовалось как "500",
	// а "100.50" — как "100.5". Целые числа без точки не трогаем.
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = strings.TrimRight(s, "0")
		if before, ok := strings.CutSuffix(s, "."); ok {
			s = before
		}
	}
	return []byte(s), nil
}

// procTimeout — таймаут на обработку одного HTTP-запроса.
const procTimeout = 5 * time.Second

// maxOrderNumberLen — ограничение на размер тела при загрузке номера заказа.
// Номер заказа — короткая строка из цифр, большие тела отсекаются.
// Значение синхронизировано с колонкой orders.number VARCHAR(32) в PostgreSQL.
const maxOrderNumberLen = 32

// maxJSONBodyLen — ограничение на размер JSON-тела для хендлеров register, login,
// withdraw. Защищает от произвольно больших тел запроса.
const maxJSONBodyLen = 4096

// ---------------------------------------------------------------------------
// Интерфейсы сервиса.
// Подробный комментарий к методам см. в '../service/service.go'.
// ---------------------------------------------------------------------------

// UserAuthService — регистрация и аутентификация пользователей.
type UserAuthService interface {
	RegisterUser(ctx context.Context, login, password string) (int64, error)
	AuthenticateUser(ctx context.Context, login, password string) (int64, error)
}

// OrderService — операции с заказами.
type OrderService interface {
	UploadOrder(ctx context.Context, orderNumber string, userID int64) error
	GetOrderByNumber(ctx context.Context, number string) (int64, error)
	ListUserOrders(ctx context.Context, userID int64) ([]service.OrderDTO, error)
}

// BalanceService — баланс и списания.
type BalanceService interface {
	GetBalance(ctx context.Context, userID int64) (decimal.Decimal, decimal.Decimal, error)
	WithdrawPoints(ctx context.Context, userID int64, orderNo string, sum decimal.Decimal) error
	ListWithdrawals(ctx context.Context, userID int64) ([]service.WithdrawalDTO, error)
}

// Pinger — проверка доступности хранилища.
type Pinger interface {
	Ping(ctx context.Context) error
}

// GopherMartService — составной интерфейс сервисного слоя, используемый хендлером.
type GopherMartService interface {
	UserAuthService
	OrderService
	BalanceService
	Pinger
}

// ---------------------------------------------------------------------------
// Конфигурация и код HTTP-роутера.
// ---------------------------------------------------------------------------

// RouterParams содержит параметры для создания HTTP-роутера.
type RouterParams struct {
	Service    GopherMartService
	Logger     *zap.Logger   // логгер для middleware и хендлеров; если nil — используется no-op
	CookieName string        // имя cookie для JWT
	CookieTTL  time.Duration // срок действия cookie
	SigningKey []byte        // ключ подписи JWT
}

// NewRouterParams создаёт RouterParams из конфигурации приложения и сервиса.
func NewRouterParams(cfg config.ApplicationConfig, svc GopherMartService, log *zap.Logger) RouterParams {
	return RouterParams{
		Service:    svc,
		Logger:     log,
		CookieName: cfg.CookieAuthName,
		CookieTTL:  cfg.CookieAuthTTL,
		SigningKey: cfg.SigningKey,
	}
}

// Router — HTTP-роутер на базе chi, объединяющий все эндпоинты сервиса.
type Router struct {
	mux        *chi.Mux
	service    GopherMartService
	log        *zap.Logger
	cookieName string
	cookieTTL  time.Duration
	signingKey []byte
}

// NewRouter создаёт и настраивает HTTP-роутер со всеми эндпоинтами сервиса.
// Подключает middleware: логирование, gzip-компрессия, аутентификация для защищённых маршрутов.
// Возвращает готовый Router.
func NewRouter(params RouterParams) *Router {
	r := chi.NewRouter()

	log := params.Logger
	if log == nil {
		log = zap.NewNop()
	}

	r.Use(logger.WithLogging(log))
	r.Use(GzipMiddleware)

	rt := &Router{
		mux:        r,
		service:    params.Service,
		log:        log,
		cookieName: params.CookieName,
		cookieTTL:  params.CookieTTL,
		signingKey: params.SigningKey,
	}

	// Публичные эндпоинты (без аутентификации)
	r.Route("/api/user", func(r chi.Router) {
		r.Post("/register", rt.register)
		r.Post("/login", rt.login)
	})

	// Защищённые эндпоинты (требуют аутентификации)
	r.Group(func(r chi.Router) {
		r.Use(RequireAuth(params.SigningKey, params.CookieName))

		r.Post("/api/user/orders", rt.uploadOrder)
		r.Get("/api/user/orders", rt.listOrders)

		r.Get("/api/user/balance", rt.getBalance)
		r.Post("/api/user/balance/withdraw", rt.withdraw)

		r.Get("/api/user/withdrawals", rt.listWithdrawals)
	})

	r.Get("/ping", rt.pingDB)

	return rt
}

// ServeHTTP делегирует обработку запроса внутреннему chi-роутеру.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt.mux.ServeHTTP(w, r)
}

// getUserIDInt64 — вспомогательная функция, извлекающая ID пользователя
// из контекста запроса и преобразующая его в int64.
// Используется во всех защищённых хендлерах.
// Возвращает ID пользователя или ошибку, если пользователь не аутентифицирован.
func getUserIDInt64(r *http.Request) (int64, error) {
	userIDStr, err := getUserIDFromContext(r.Context())
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(userIDStr, 10, 64)
}

// register обрабатывает POST /api/user/register — регистрацию нового пользователя.
// Принимает JSON с полями login и password. При успехе устанавливает
// cookie с JWT-токеном и возвращает 200. Если логин занят — 409.
func (rt *Router) register(w http.ResponseWriter, r *http.Request) {
	// Ограничиваем размер JSON-тела для защиты от произвольно больших запросов.
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyLen)

	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "неверный формат запроса", http.StatusBadRequest)
		return
	}

	if req.Login == "" || req.Password == "" {
		http.Error(w, "логин и пароль обязательны", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), procTimeout)
	defer cancel()

	userID, err := rt.service.RegisterUser(ctx, req.Login, req.Password)
	if err != nil {
		if errors.Is(err, service.ErrLoginTaken) {
			http.Error(w, "логин уже занят", http.StatusConflict)
			return
		}
		rt.log.Error("ошибка регистрации", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if err := setAuthCookie(w, rt.cookieName, rt.cookieTTL, rt.signingKey, strconv.FormatInt(userID, 10)); err != nil {
		rt.log.Error("ошибка установки cookie аутентификации", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// login обрабатывает POST /api/user/login — аутентификацию пользователя.
// Принимает JSON с полями login и password. При успехе устанавливает
// cookie с JWT-токеном и возвращает 200. При неверных данных — 401.
func (rt *Router) login(w http.ResponseWriter, r *http.Request) {
	// Ограничиваем размер JSON-тела для защиты от произвольно больших запросов.
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyLen)

	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "неверный формат запроса", http.StatusBadRequest)
		return
	}

	if req.Login == "" || req.Password == "" {
		http.Error(w, "логин и пароль обязательны", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), procTimeout)
	defer cancel()

	userID, err := rt.service.AuthenticateUser(ctx, req.Login, req.Password)
	if err != nil {
		if errors.Is(err, service.ErrInvalidCredentials) {
			http.Error(w, "неверная пара логин/пароль", http.StatusUnauthorized)
			return
		}
		rt.log.Error("ошибка аутентификации", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if err := setAuthCookie(w, rt.cookieName, rt.cookieTTL, rt.signingKey, strconv.FormatInt(userID, 10)); err != nil {
		rt.log.Error("ошибка установки cookie аутентификации", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// uploadOrder обрабатывает POST /api/user/orders — загрузку номера заказа.
// Принимает номер заказа в виде plain text.
// Проверяет номер по алгоритму Луна (отдает код 422 в случае неуспеха).
// Если заказ уже загружен этим пользователем отвечает кодом 200, другим — 409.
// Новый заказ — 202 и постановка в очередь на опрос accrual-системы.
func (rt *Router) uploadOrder(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDInt64(r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	// Ограничиваем размер тела, чтобы защититься от произвольного объёма данных.
	// Номер заказа — короткая строка из цифр.
	r.Body = http.MaxBytesReader(w, r.Body, maxOrderNumberLen)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "ошибка чтения тела запроса", http.StatusBadRequest)
		return
	}

	// Обрезаем пробелы и символы переноса строки — например, при отправке через curl
	// тело может содержать trailing newline, из-за которого проверка Луна не пройдёт.
	orderNumber := strings.TrimSpace(string(body))
	if orderNumber == "" {
		http.Error(w, "номер заказа не передан", http.StatusBadRequest)
		return
	}

	if !luhn.IsValid(orderNumber) {
		http.Error(w, "неверный формат номера заказа", http.StatusUnprocessableEntity)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), procTimeout)
	defer cancel()

	err = rt.service.UploadOrder(ctx, orderNumber, userID)
	if err != nil {
		// Заказ уже существует — определяем владельца одним запросом в БД.
		if errors.Is(err, service.ErrOrderAlreadyExists) {
			ownerID, ownerErr := rt.service.GetOrderByNumber(ctx, orderNumber)
			if ownerErr != nil {
				rt.log.Error("ошибка определения владельца заказа", zap.Error(ownerErr))
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				return
			}

			if ownerID == userID {
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Error(w, "номер заказа уже загружен другим пользователем", http.StatusConflict)
			return
		}
		rt.log.Error("ошибка загрузки заказа", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// jsonTime — обёртка над time.Time, сериализуется в JSON как RFC3339
// без дробных секунд. Стандартный маршалер time.Time использует RFC3339Nano
// (с дробными секундами), что может не совпадать с ожиданиями тест-харнесса,
// если он сравнивает строки строго.
type jsonTime time.Time

// MarshalJSON сериализует время в формате RFC3339 (без дробных секунд).
func (t jsonTime) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Time(t).Format(time.RFC3339) + `"`), nil
}

// listOrders обрабатывает GET /api/user/orders — получение списка заказов пользователя
// со статусами обработки и информацией о начислениях.
// Возвращает JSON-массив, отсортированный от новых к старым.
// Если заказов нет — 204.
func (rt *Router) listOrders(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDInt64(r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), procTimeout)
	defer cancel()

	orders, err := rt.service.ListUserOrders(ctx, userID)
	if err != nil {
		rt.log.Error("ошибка получения списка заказов", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if len(orders) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set(ContentType, AppJSON)
	w.WriteHeader(http.StatusOK)

	type orderItem struct {
		Number     string      `json:"number"`
		Status     string      `json:"status"`
		Accrual    *jsonNumber `json:"accrual,omitempty"`
		UploadedAt jsonTime    `json:"uploaded_at"`
	}

	items := make([]orderItem, 0, len(orders))
	for _, o := range orders {
		item := orderItem{
			Number:     o.Number,
			Status:     o.Status,
			UploadedAt: jsonTime(o.UploadedAt),
		}
		// accrual включается только для PROCESSED-заказов с положительным значением
		if o.Status == "PROCESSED" && o.Accrual.GreaterThan(decimal.Zero) {
			accrual := jsonNumber(o.Accrual)
			item.Accrual = &accrual
		}
		items = append(items, item)
	}

	if err := json.NewEncoder(w).Encode(items); err != nil {
		rt.log.Error("ошибка кодирования ответа со списком заказов", zap.Error(err))
	}
}

// getBalance обрабатывает GET /api/user/balance — получение текущего баланса
// и суммы всех списаний пользователя. Возвращает JSON с полями current и withdrawn.
func (rt *Router) getBalance(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDInt64(r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), procTimeout)
	defer cancel()

	balance, withdrawn, err := rt.service.GetBalance(ctx, userID)
	if err != nil {
		rt.log.Error("ошибка получения баланса", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Set(ContentType, AppJSON)
	w.WriteHeader(http.StatusOK)

	resp := struct {
		Current   jsonNumber `json:"current"`
		Withdrawn jsonNumber `json:"withdrawn"`
	}{
		Current:   jsonNumber(balance),
		Withdrawn: jsonNumber(withdrawn),
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		rt.log.Error("ошибка кодирования ответа с балансом", zap.Error(err))
	}
}

// withdraw обрабатывает POST /api/user/balance/withdraw — списание баллов
// в счёт оплаты нового заказа. Принимает JSON с полями order и sum.
// Проверяет номер по Луну (код 422 при неуспехе),
// проверяет достаточность баланса (код 402 при нехватке),
// создаёт заказ и запись о списании в одной транзакции.
func (rt *Router) withdraw(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDInt64(r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	// Ограничиваем размер JSON-тела для защиты от произвольно больших запросов.
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyLen)

	var req struct {
		Order string          `json:"order"`
		Sum   decimal.Decimal `json:"sum"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "неверный формат запроса", http.StatusBadRequest)
		return
	}

	if req.Order == "" {
		http.Error(w, "номер заказа не передан", http.StatusBadRequest)
		return
	}

	if !luhn.IsValid(req.Order) {
		http.Error(w, "неверный номер заказа", http.StatusUnprocessableEntity)
		return
	}

	if req.Sum.LessThanOrEqual(decimal.Zero) {
		http.Error(w, "сумма списания должна быть положительной", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), procTimeout)
	defer cancel()

	err = rt.service.WithdrawPoints(ctx, userID, req.Order, req.Sum)
	if err != nil {
		if errors.Is(err, service.ErrInsufficientFunds) {
			http.Error(w, "на счету недостаточно средств", http.StatusPaymentRequired)
			return
		}

		// Код 422 используется здесь расширительно:
		// спецификация описывает 422 как "неверный номер заказа",
		// но 409 (Conflict) отсутствует в списке допустимых
		// кодов для этого эндпоинта.
		if errors.Is(err, service.ErrOrderAlreadyExists) {
			http.Error(w, "номер заказа уже использован", http.StatusUnprocessableEntity)
			return
		}

		rt.log.Error("ошибка списания баллов", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// listWithdrawals обрабатывает GET /api/user/withdrawals — получение списка
// всех списаний пользователя. Возвращает JSON-массив, отсортированный
// от новых к старым. Если списаний нет — 204.
func (rt *Router) listWithdrawals(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDInt64(r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), procTimeout)
	defer cancel()

	withdrawals, err := rt.service.ListWithdrawals(ctx, userID)
	if err != nil {
		rt.log.Error("ошибка получения списка списаний", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if len(withdrawals) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set(ContentType, AppJSON)
	w.WriteHeader(http.StatusOK)

	type withdrawalItem struct {
		Order       string     `json:"order"`
		Sum         jsonNumber `json:"sum"`
		ProcessedAt jsonTime   `json:"processed_at"`
	}

	items := make([]withdrawalItem, 0, len(withdrawals))
	for _, wl := range withdrawals {
		items = append(items, withdrawalItem{
			Order:       wl.OrderNumber,
			Sum:         jsonNumber(wl.Sum),
			ProcessedAt: jsonTime(wl.ProcessedAt),
		})
	}

	if err := json.NewEncoder(w).Encode(items); err != nil {
		rt.log.Error("ошибка кодирования ответа со списком списаний", zap.Error(err))
	}
}

// pingDB обрабатывает GET /ping — проверку доступности хранилища данных.
// Возвращает 200 при успехе, 500 при недоступности.
func (rt *Router) pingDB(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), procTimeout)
	defer cancel()

	if err := rt.service.Ping(ctx); err != nil {
		rt.log.Error("ошибка проверки доступности базы данных", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
