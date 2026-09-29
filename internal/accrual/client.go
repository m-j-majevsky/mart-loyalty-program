package accrual

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// OrderResponse — структура ответа внешней системы расчёта начислений.
type OrderResponse struct {
	Order   string          `json:"order"`
	Status  string          `json:"status"`
	Accrual decimal.Decimal `json:"accrual"`
}

// Параметры ретраев для 5xx-ошибок.
const (
	maxRetries = 3               // максимальное число повторных попыток
	retryDelay = 1 * time.Second // задержка между попытками
)

// Client — HTTP-клиент для взаимодействия с внешней системой расчёта баллов.
type Client struct {
	baseURL    string
	httpClient *http.Client
	log        *zap.Logger
}

// NewClient создаёт HTTP-клиент для accrual-системы по адресу baseURL.
// Использует таймаут 5 секунд на каждый HTTP-запрос.
// log — логгер для диагностики (предупреждения о нестандартных заголовках и т.п.).
func NewClient(baseURL string, log *zap.Logger) *Client {
	if log == nil {
		log = zap.NewNop()
	}
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		log: log,
	}
}

// GetOrderAccrual запрашивает у accrual-системы информацию о расчёте начисления
// для заказа с указанным номером orderNumber.
// Возвращает:
//   - структуру OrderResponse и nil, если получен ответ 200;
//   - nil и ErrNotRegistered, если получен ответ 204 (заказ не зарегистрирован);
//   - nil и ErrTooManyRequests с указанием задержки из Retry-After, если получен 429;
//   - nil и ошибку для прочих кодов ответа.
//
// При ответах 5xx выполняется до maxRetries повторных попыток с задержкой retryDelay
// между ними. Это позволяет восстановиться за секунды при транзиентных сбоях
// accrual-системы, не передавая ответственность за ретрай сервисному слою.
func (c *Client) GetOrderAccrual(ctx context.Context, orderNumber string) (*OrderResponse, error) {
	url := fmt.Sprintf("%s/api/orders/%s", c.baseURL, orderNumber)

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			c.log.Debug("ретрай запроса к accrual-системе",
				zap.String("заказ", orderNumber),
				zap.Int("попытка", attempt))

			select {
			case <-time.After(retryDelay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		resp, err := c.doRequest(ctx, url)
		if err != nil {
			lastErr = err
			// Ошибки сети/транспорта — retry
			continue
		}

		result, handled, err := c.handleResponse(resp, orderNumber)
		if handled {
			return result, err
		}

		// Необработанный код — если это 5xx, retry; иначе — финальная ошибка
		lastErr = err
		if resp.StatusCode < 500 || resp.StatusCode >= 600 {
			return nil, err
		}
	}

	return nil, fmt.Errorf("accrual-система недоступна после %d попыток: %w", maxRetries+1, lastErr)
}

// doRequest выполняет один HTTP-запрос к accrual-системе.
func (c *Client) doRequest(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("ошибка создания запроса к accrual-системе: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка запроса к accrual-системе: %w", err)
	}

	return resp, nil
}

// handleResponse обрабатывает HTTP-ответ accrual-системы.
// Возвращает (результат, true, ошибка), если ответ обработан (финальный статус — не ретраится).
// Возвращает (nil, false, ошибка), если ответ не обработан — caller решает, ретраить или нет.
func (c *Client) handleResponse(resp *http.Response, orderNumber string) (*OrderResponse, bool, error) {
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, true, fmt.Errorf("ошибка чтения ответа accrual-системы: %w", err)
		}

		var result OrderResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, true, fmt.Errorf("ошибка разбора JSON ответа accrual-системы: %w", err)
		}

		return &result, true, nil

	case http.StatusNoContent:
		return nil, true, NewErrNotRegistered()

	case http.StatusTooManyRequests:
		retryAfter := 1
		if v := resp.Header.Get("Retry-After"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil {
				retryAfter = parsed
			} else {
				c.log.Warn("некорректное значение заголовка Retry-After, используется значение по умолчанию",
					zap.String("заказ", orderNumber),
					zap.String("retry_after", v),
					zap.Error(err),
					zap.Int("по_умолчанию_сек", retryAfter))
			}
		}
		return nil, true, NewErrTooManyRequests(retryAfter)

	default:
		return nil, false, fmt.Errorf("неожиданный код ответа от accrual-системы: %d", resp.StatusCode)
	}
}

// ErrNotRegistered означает, что заказ не зарегистрирован в accrual-системе.
type ErrNotRegistered struct{}

func (e *ErrNotRegistered) Error() string {
	return "заказ не зарегистрирован в accrual-системе"
}

func NewErrNotRegistered() error {
	return &ErrNotRegistered{}
}

// ErrTooManyRequests означает, что превышен лимит запросов к accrual-системе.
// Поле RetryAfter содержит рекомендованную задержку в секундах.
type ErrTooManyRequests struct {
	RetryAfter int
}

func (e *ErrTooManyRequests) Error() string {
	return fmt.Sprintf("превышен лимит запросов к accrual-системе, повтор через %d секунд", e.RetryAfter)
}

func NewErrTooManyRequests(retryAfterInSeconds int) error {
	return &ErrTooManyRequests{RetryAfter: retryAfterInSeconds}
}
