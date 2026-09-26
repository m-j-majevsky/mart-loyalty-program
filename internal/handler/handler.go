package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/m-j-majevsky/gophermart/internal/config"
	"github.com/m-j-majevsky/gophermart/internal/logger"
	"github.com/m-j-majevsky/gophermart/internal/luhn"
	"github.com/m-j-majevsky/gophermart/internal/repository"
	"github.com/m-j-majevsky/gophermart/internal/service"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// procTimeout — таймаут на обработку одного HTTP-запроса.
const procTimeout = 5 * time.Second

// GopherMartService — интерфейс сервисного слоя, используемый хендлером.
// Содержит только те методы, которые нужны HTTP-обработчикам.
type GopherMartService interface {
	RegisterUser(ctx context.Context, login, password string) (int64, error)
	AuthenticateUser(ctx context.Context, login, password string) (int64, error)
	UploadOrder(ctx context.Context, orderNumber string, userID int64) error
	WithdrawPoints(ctx context.Context, userID int64, orderNo string, sum decimal.Decimal) error
	ListUserOrders(ctx context.Context, userID int64) ([]repository.Order, error)
	GetBalance(ctx context.Context, userID int64) (decimal.Decimal, decimal.Decimal, error)
	ListWithdrawals(ctx context.Context, userID int64) ([]repository.Withdrawal, error)
	GetOrderByNumber(ctx context.Context, number string) (int64, error)
	Ping(ctx context.Context) error
}

// RouterParams содержит параметры для создания HTTP-роутера.
type RouterParams struct {
	Service    GopherMartService
	CookieName string        // имя cookie для JWT
	CookieTTL  time.Duration // срок действия cookie
	SigningKey []byte        // ключ подписи JWT
}

// NewRouterParams создаёт RouterParams из конфигурации приложения и сервиса.
func NewRouterParams(cfg config.ApplicationConfig, svc GopherMartService) RouterParams {
	return RouterParams{
		Service:    svc,
		CookieName: cfg.CookieAuthName,
		CookieTTL:  cfg.CookieAuthTTL,
		SigningKey: cfg.SigningKey,
	}
}

// Router — HTTP-роутер на базе chi, объединяющий все эндпоинты сервиса.
type Router struct {
	mux        *chi.Mux
	service    GopherMartService
	cookieName string
	cookieTTL  time.Duration
	signingKey []byte
}

// NewRouter создаёт и настраивает HTTP-роутер со всеми эндпоинтами сервиса.
// Подключает middleware: логирование, gzip-компрессия, аутентификация
// для защищённых маршрутов. Возвращает готовый Router.
func NewRouter(params RouterParams) (*Router, error) {
	r := chi.NewRouter()

	r.Use(logger.WithLogging)
	r.Use(GzipMiddleware)

	rt := &Router{
		mux:        r,
		service:    params.Service,
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

	return rt, nil
}

// ServeHTTP делегирует обработку запроса внутреннему chi-роутеру.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt.mux.ServeHTTP(w, r)
}

// register обрабатывает POST /api/user/register — регистрацию нового пользователя.
// Принимает JSON с полями login и password. При успехе устанавливает cookie
// с JWT-токеном и возвращает 200. Если логин занят — 409.
func (rt *Router) register(w http.ResponseWriter, r *http.Request) {
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
		logger.Log.Error("register failed", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if err := setAuthCookie(w, rt.cookieName, rt.cookieTTL, rt.signingKey, strconv.FormatInt(userID, 10)); err != nil {
		logger.Log.Error("set auth cookie failed", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// login обрабатывает POST /api/user/login — аутентификацию пользователя.
// Принимает JSON с полями login и password. При успехе устанавливает cookie
// с JWT-токеном и возвращает 200. При неверных данных — 401.
func (rt *Router) login(w http.ResponseWriter, r *http.Request) {
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
		logger.Log.Error("login failed", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if err := setAuthCookie(w, rt.cookieName, rt.cookieTTL, rt.signingKey, strconv.FormatInt(userID, 10)); err != nil {
		logger.Log.Error("set auth cookie failed", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// uploadOrder обрабатывает POST /api/user/orders — загрузку номера заказа.
// Принимает номер заказа в виде plain text. Проверяет номер по алгоритму Луна (422).
// Если заказ уже загружен этим пользователем — 200, другим — 409.
// Новый заказ — 202 и постановка в очередь на опрос accrual-системы.
func (rt *Router) uploadOrder(w http.ResponseWriter, r *http.Request) {
	userIDStr, err := getUserIDFromContext(r.Context())
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "ошибка чтения тела запроса", http.StatusBadRequest)
		return
	}

	orderNumber := string(body)
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
		var eoae *repository.ErrOrderAlreadyExists
		if errors.As(err, &eoae) {
			// Заказ уже существует — определяем владельца одним запросом
			ownerID, ownerErr := rt.service.GetOrderByNumber(ctx, orderNumber)
			if ownerErr != nil {
				logger.Log.Error("get order owner failed", zap.Error(ownerErr))
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
		logger.Log.Error("upload order failed", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// listOrders обрабатывает GET /api/user/orders — получение списка заказов пользователя
// со статусами обработки и информацией о начислениях.
// Возвращает JSON-массив, отсортированный от новых к старым.
// Если заказов нет — 204.
func (rt *Router) listOrders(w http.ResponseWriter, r *http.Request) {
	userIDStr, err := getUserIDFromContext(r.Context())
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), procTimeout)
	defer cancel()

	orders, err := rt.service.ListUserOrders(ctx, userID)
	if err != nil {
		logger.Log.Error("list orders failed", zap.Error(err))
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
		Number     string           `json:"number"`
		Status     string           `json:"status"`
		Accrual    *decimal.Decimal `json:"accrual,omitempty"`
		UploadedAt time.Time        `json:"uploaded_at"`
	}

	items := make([]orderItem, 0, len(orders))
	for _, o := range orders {
		item := orderItem{
			Number:     o.Number,
			Status:     o.Status,
			UploadedAt: o.UploadedAt,
		}
		// accrual включается только для PROCESSED-заказов с положительным значением
		if o.Status == "PROCESSED" && o.Accrual.GreaterThan(decimal.Zero) {
			accrual := o.Accrual
			item.Accrual = &accrual
		}
		items = append(items, item)
	}

	if err := json.NewEncoder(w).Encode(items); err != nil {
		logger.Log.Error("encode orders response failed", zap.Error(err))
	}
}

// getBalance обрабатывает GET /api/user/balance — получение текущего баланса
// и суммы всех списаний пользователя. Возвращает JSON с полями current и withdrawn.
func (rt *Router) getBalance(w http.ResponseWriter, r *http.Request) {
	userIDStr, err := getUserIDFromContext(r.Context())
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), procTimeout)
	defer cancel()

	balance, withdrawn, err := rt.service.GetBalance(ctx, userID)
	if err != nil {
		logger.Log.Error("get balance failed", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Set(ContentType, AppJSON)
	w.WriteHeader(http.StatusOK)

	resp := struct {
		Current   decimal.Decimal `json:"current"`
		Withdrawn decimal.Decimal `json:"withdrawn"`
	}{
		Current:   balance,
		Withdrawn: withdrawn,
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Log.Error("encode balance response failed", zap.Error(err))
	}
}

// withdraw обрабатывает POST /api/user/balance/withdraw — списание баллов
// в счёт оплаты нового заказа. Принимает JSON с полями order и sum.
// Проверяет номер по Луну (422), проверяет достаточность баланса (402),
// создаёт заказ и запись о списании в одной транзакции, ставит заказ
// в очередь на опрос accrual-системы.
func (rt *Router) withdraw(w http.ResponseWriter, r *http.Request) {
	userIDStr, err := getUserIDFromContext(r.Context())
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

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
		var eif *repository.ErrInsufficientFunds
		if errors.As(err, &eif) {
			http.Error(w, "на счету недостаточно средств", http.StatusPaymentRequired)
			return
		}

		var eoae *repository.ErrOrderAlreadyExists
		if errors.As(err, &eoae) {
			http.Error(w, "заказ уже зарегистрирован", http.StatusUnprocessableEntity)
			return
		}

		logger.Log.Error("withdraw failed", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// listWithdrawals обрабатывает GET /api/user/withdrawals — получение списка
// всех списаний пользователя. Возвращает JSON-массив, отсортированный
// от новых к старым. Если списаний нет — 204.
func (rt *Router) listWithdrawals(w http.ResponseWriter, r *http.Request) {
	userIDStr, err := getUserIDFromContext(r.Context())
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), procTimeout)
	defer cancel()

	withdrawals, err := rt.service.ListWithdrawals(ctx, userID)
	if err != nil {
		logger.Log.Error("list withdrawals failed", zap.Error(err))
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
		Order       string          `json:"order"`
		Sum         decimal.Decimal `json:"sum"`
		ProcessedAt time.Time       `json:"processed_at"`
	}

	items := make([]withdrawalItem, 0, len(withdrawals))
	for _, w := range withdrawals {
		items = append(items, withdrawalItem{
			Order:       w.OrderNumber,
			Sum:         w.Sum,
			ProcessedAt: w.ProcessedAt,
		})
	}

	if err := json.NewEncoder(w).Encode(items); err != nil {
		logger.Log.Error("encode withdrawals response failed", zap.Error(err))
	}
}

// pingDB обрабатывает GET /ping — проверку доступности хранилища данных.
// Возвращает 200 при успехе, 500 при недоступности.
func (rt *Router) pingDB(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), procTimeout)
	defer cancel()

	if err := rt.service.Ping(ctx); err != nil {
		logger.Log.Error("database ping failed", zap.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
