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
)

// OrderResponse — структура ответа внешней системы расчёта начислений.
type OrderResponse struct {
	Order   string          `json:"order"`
	Status  string          `json:"status"`
	Accrual decimal.Decimal `json:"accrual"`
}

// Client — HTTP-клиент для взаимодействия с внешней системой расчёта баллов.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient создаёт HTTP-клиент для accrual-системы по адресу baseURL.
// Использует таймаут 5 секунд на каждый HTTP-запрос.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// GetOrderAccrual запрашивает у accrual-системы информацию о расчёте начисления
// для заказа с указанным номером orderNumber.
// Возвращает:
//   - структуру OrderResponse и nil, если получен ответ 200;
//   - nil и ErrNotRegistered, если получен ответ 204 (заказ не зарегистрирован);
//   - nil и ErrTooManyRequests с указанием задержки из Retry-After, если получен 429;
//   - nil и ошибку для прочих кодов ответа.
func (c *Client) GetOrderAccrual(ctx context.Context, orderNumber string) (*OrderResponse, error) {
	url := fmt.Sprintf("%s/api/orders/%s", c.baseURL, orderNumber)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("ошибка создания запроса к accrual-системе: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка запроса к accrual-системе: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("ошибка чтения ответа accrual-системы: %w", err)
		}

		var result OrderResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("ошибка разбора JSON ответа accrual-системы: %w", err)
		}

		return &result, nil

	case http.StatusNoContent:
		return nil, NewErrNotRegistered()

	case http.StatusTooManyRequests:
		retryAfter := 1
		if v := resp.Header.Get("Retry-After"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil {
				retryAfter = parsed
			}
		}
		return nil, NewErrTooManyRequests(retryAfter)

	default:
		return nil, fmt.Errorf("неожиданный код ответа от accrual-системы: %d", resp.StatusCode)
	}
}

// ErrNotRegistered означает, что заказ не зарегистрирован в accrual-системе.
type ErrNotRegistered struct{}

// Error возвращает текстовое описание ошибки.
func (e *ErrNotRegistered) Error() string {
	return "заказ не зарегистрирован в accrual-системе"
}

// NewErrNotRegistered конструирует ошибку ErrNotRegistered.
func NewErrNotRegistered() error {
	return &ErrNotRegistered{}
}

// ErrTooManyRequests означает, что превышен лимит запросов к accrual-системе.
// Поле RetryAfter содержит рекомендованную задержку в секундах.
type ErrTooManyRequests struct {
	RetryAfter int
}

// Error возвращает текстовое описание ошибки.
func (e *ErrTooManyRequests) Error() string {
	return fmt.Sprintf("превышен лимит запросов к accrual-системе, повтор через %d секунд", e.RetryAfter)
}

// NewErrTooManyRequests конструирует ошибку ErrTooManyRequests
// с указанием задержки retryAfterInSeconds.
func NewErrTooManyRequests(retryAfterInSeconds int) error {
	return &ErrTooManyRequests{RetryAfter: retryAfterInSeconds}
}
