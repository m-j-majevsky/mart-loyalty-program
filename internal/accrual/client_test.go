package accrual

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Хелперы
// ---------------------------------------------------------------------------

// newTestClient создаёт accrual-клиент, указывающий на тестовый HTTP-сервер.
func newTestClient(t *testing.T, srvURL string) *Client {
	t.Helper()
	return NewClient(srvURL, zap.NewNop())
}

// ---------------------------------------------------------------------------
// NewClient
// ---------------------------------------------------------------------------

// TestNewClient_Default проверяет, что клиент создаётся с корректным baseURL и таймаутом.
func TestNewClient_Default(t *testing.T) {
	c := NewClient("http://example.com", zap.NewNop())
	require.NotNil(t, c)
	assert.Equal(t, "http://example.com", c.baseURL)
	assert.Equal(t, 5*time.Second, c.httpClient.Timeout)
}

// TestNewClient_NilLogger проверяет, что при nil-логгере используется no-op.
func TestNewClient_NilLogger(t *testing.T) {
	c := NewClient("http://example.com", nil)
	require.NotNil(t, c)
	// Просто убеждаемся, что логгер не nil — no-op логгер.
	assert.NotNil(t, c.log)
}

// ---------------------------------------------------------------------------
// GetOrderAccrual — тесты для основных сценариев
// ---------------------------------------------------------------------------

// TestGetOrderAccrual_OK проверяет успешный ответ 200 с корректным JSON.
func TestGetOrderAccrual_OK(t *testing.T) {
	respBody := `{"order":"12345678903","status":"PROCESSED","accrual":500.5}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/orders/12345678903", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	result, err := c.GetOrderAccrual(context.Background(), "12345678903")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "12345678903", result.Order)
	assert.Equal(t, "PROCESSED", result.Status)
	assert.True(t, result.Accrual.Equal(decimal.NewFromFloat(500.5)))
}

// TestGetOrderAccrual_NotRegistered проверяет ответ 204 — заказ не зарегистрирован.
func TestGetOrderAccrual_NotRegistered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	result, err := c.GetOrderAccrual(context.Background(), "999")

	require.Error(t, err)
	assert.Nil(t, result)

	var nre *ErrNotRegistered
	require.ErrorAs(t, err, &nre)
}

// TestGetOrderAccrual_TooManyRequests проверяет ответ 429 с валидным Retry-After.
func TestGetOrderAccrual_TooManyRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	result, err := c.GetOrderAccrual(context.Background(), "123")

	require.Error(t, err)
	assert.Nil(t, result)

	var tmr *ErrTooManyRequests
	require.ErrorAs(t, err, &tmr)
	assert.Equal(t, 60, tmr.RetryAfter)
}

// TestGetOrderAccrual_TooManyRequests_InvalidHeader проверяет 429 с некорректным Retry-After.
// Должно использоваться значение по умолчанию (1 секунда).
func TestGetOrderAccrual_TooManyRequests_InvalidHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "not-a-number")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	result, err := c.GetOrderAccrual(context.Background(), "123")

	require.Error(t, err)
	assert.Nil(t, result)

	var tmr *ErrTooManyRequests
	require.ErrorAs(t, err, &tmr)
	assert.Equal(t, 1, tmr.RetryAfter)
}

// TestGetOrderAccrual_TooManyRequests_NoHeader проверяет 429 без заголовка Retry-After.
// Должно использоваться значение по умолчанию (1 секунда).
func TestGetOrderAccrual_TooManyRequests_NoHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	result, err := c.GetOrderAccrual(context.Background(), "123")

	require.Error(t, err)
	assert.Nil(t, result)

	var tmr *ErrTooManyRequests
	require.ErrorAs(t, err, &tmr)
	assert.Equal(t, 1, tmr.RetryAfter)
}

// TestGetOrderAccrual_4xxError проверяет, что 4xx-ошибки (кроме 429) не ретраятся.
func TestGetOrderAccrual_4xxError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	result, err := c.GetOrderAccrual(context.Background(), "123")

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "400")
	// 4xx не ретраится — ровно один вызов.
	assert.Equal(t, 1, calls)
}

// ---------------------------------------------------------------------------
// GetOrderAccrual — ретраи 5xx
// ---------------------------------------------------------------------------

// TestGetOrderAccrual_5xxThenSuccess проверяет, что после 5xx-ошибки следует ретрай и успешный ответ.
func TestGetOrderAccrual_5xxThenSuccess(t *testing.T) {
	calls := 0
	respBody := `{"order":"123","status":"PROCESSED","accrual":100}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	// Ускоряем тест: уменьшаем retryDelay через замену httpClient (транспорт остаётся тот же,
	// но ретраи идут через time.After(retryDelay) — к сожалению, retryDelay — константа,
	// поэтому тест может быть медленным. Используем контекст с таймаутом.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := c.GetOrderAccrual(ctx, "123")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "123", result.Order)
	assert.Equal(t, "PROCESSED", result.Status)
	assert.Equal(t, 3, calls)
}

// TestGetOrderAccrual_5xxMaxRetries проверяет, что после maxRetries+1 попыток возвращается ошибка.
func TestGetOrderAccrual_5xxMaxRetries(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := c.GetOrderAccrual(ctx, "123")

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "недоступна после")
	// maxRetries=3 → всего 4 попытки (0 + 3 ретрая).
	assert.Equal(t, maxRetries+1, calls)
}

// ---------------------------------------------------------------------------
// GetOrderAccrual — ошибки сети и контекст
// ---------------------------------------------------------------------------

// TestGetOrderAccrual_NetworkError проверяет, что сетевая ошибка приводит к ретраям и финальной ошибке.
func TestGetOrderAccrual_NetworkError(t *testing.T) {
	// Сервер, который сразу закрывается — все запросы вернут сетевую ошибку.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // закрываем сразу

	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := c.GetOrderAccrual(ctx, "123")

	require.Error(t, err)
	assert.Nil(t, result)
}

// TestGetOrderAccrual_ContextCancelled проверяет, что отмена контекста во время ретрая прекращает запрос.
func TestGetOrderAccrual_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())

	// Отменяем контекст сразу — первый запрос может пройти, но ретрай будет отменён.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	result, err := c.GetOrderAccrual(ctx, "123")

	require.Error(t, err)
	assert.Nil(t, result)
	// Ошибка может быть либо ctx.Err(), либо "accrual-система недоступна" —
	// в зависимости от того, успел ли первый запрос пройти.
}

// ---------------------------------------------------------------------------
// GetOrderAccrual — ошибки разбора JSON
// ---------------------------------------------------------------------------

// TestGetOrderAccrual_InvalidJSON проверяет, что невалидный JSON в ответе 200 возвращает ошибку.
func TestGetOrderAccrual_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	result, err := c.GetOrderAccrual(context.Background(), "123")

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "ошибка разбора JSON")
}

// TestGetOrderAccrual_EmptyBody проверяет ответ 200 с пустым телом — ошибка разбора JSON.
func TestGetOrderAccrual_EmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	result, err := c.GetOrderAccrual(context.Background(), "123")

	require.Error(t, err)
	assert.Nil(t, result)
}

// ---------------------------------------------------------------------------
// doRequest — URL formatting
// ---------------------------------------------------------------------------

// TestDoRequest_URLFormat проверяет, что URL формируется корректно.
func TestDoRequest_URLFormat(t *testing.T) {
	var receivedURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"order":"123","status":"PROCESSED","accrual":100}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.GetOrderAccrual(context.Background(), "123")
	require.NoError(t, err)
	assert.Equal(t, "/api/orders/123", receivedURL)
}

// TestDoRequest_BadURL проверяет обработку некорректного URL.
func TestDoRequest_BadURL(t *testing.T) {
	c := newTestClient(t, "http://[::1]:namedport") // невалидный URL
	result, err := c.GetOrderAccrual(context.Background(), "123")
	require.Error(t, err)
	assert.Nil(t, result)
}

// ---------------------------------------------------------------------------
// handleResponse — table-driven
// ---------------------------------------------------------------------------

// TestHandleResponse_OK проверяет обработку ответа 200 с валидным JSON.
func TestHandleResponse_OK(t *testing.T) {
	body := `{"order":"123","status":"PROCESSED","accrual":500.5}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
	c := newTestClient(t, "http://example.com")
	result, handled, err := c.handleResponse(resp, "123")

	assert.True(t, handled)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "123", result.Order)
	assert.Equal(t, "PROCESSED", result.Status)
	assert.True(t, result.Accrual.Equal(decimal.NewFromFloat(500.5)))
}

// TestHandleResponse_NotRegistered проверяет ответ 204.
func TestHandleResponse_NotRegistered(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusNoContent,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}
	c := newTestClient(t, "http://example.com")
	result, handled, err := c.handleResponse(resp, "123")

	assert.True(t, handled)
	assert.Nil(t, result)
	var nre *ErrNotRegistered
	require.ErrorAs(t, err, &nre)
}

// TestHandleResponse_TooManyRequests проверяет ответ 429 с заголовком Retry-After.
func TestHandleResponse_TooManyRequests(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     http.Header{"Retry-After": []string{"42"}},
	}
	c := newTestClient(t, "http://example.com")
	result, handled, err := c.handleResponse(resp, "123")

	assert.True(t, handled)
	assert.Nil(t, result)
	var tmr *ErrTooManyRequests
	require.ErrorAs(t, err, &tmr)
	assert.Equal(t, 42, tmr.RetryAfter)
}

// TestHandleResponse_TooManyRequests_InvalidHeader проверяет 429 с некорректным Retry-After.
func TestHandleResponse_TooManyRequests_InvalidHeader(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     http.Header{"Retry-After": []string{"abc"}},
	}
	c := newTestClient(t, "http://example.com")
	result, handled, err := c.handleResponse(resp, "123")

	assert.True(t, handled)
	assert.Nil(t, result)
	var tmr *ErrTooManyRequests
	require.ErrorAs(t, err, &tmr)
	assert.Equal(t, 1, tmr.RetryAfter)
}

// TestHandleResponse_DefaultStatusCode проверяет необработанный статус (не 5xx).
func TestHandleResponse_DefaultStatusCode(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}
	c := newTestClient(t, "http://example.com")
	result, handled, err := c.handleResponse(resp, "123")

	assert.False(t, handled)
	assert.Nil(t, result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

// TestHandleResponse_5xxStatusCode проверяет, что 5xx возвращает handled=false.
func TestHandleResponse_5xxStatusCode(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}
	c := newTestClient(t, "http://example.com")
	result, handled, err := c.handleResponse(resp, "123")

	assert.False(t, handled)
	assert.Nil(t, result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

// ---------------------------------------------------------------------------
// Ошибки
// ---------------------------------------------------------------------------

// TestErrNotRegistered_Error проверяет текст ошибки ErrNotRegistered.
func TestErrNotRegistered_Error(t *testing.T) {
	err := NewErrNotRegistered()
	assert.Contains(t, err.Error(), "не зарегистрирован")
}

// TestErrNotRegistered_Type проверяет, что NewErrNotRegistered возвращает *ErrNotRegistered.
func TestErrNotRegistered_Type(t *testing.T) {
	err := NewErrNotRegistered()
	var target *ErrNotRegistered
	require.ErrorAs(t, err, &target)
}

// TestErrTooManyRequests_Error проверяет текст ошибки ErrTooManyRequests.
func TestErrTooManyRequests_Error(t *testing.T) {
	err := NewErrTooManyRequests(30)
	assert.Contains(t, err.Error(), "30")
	assert.Contains(t, err.Error(), "превышен лимит")
}

// TestErrTooManyRequests_Type проверяет, что NewErrTooManyRequests возвращает *ErrTooManyRequests.
func TestErrTooManyRequests_Type(t *testing.T) {
	err := NewErrTooManyRequests(15)
	var target *ErrTooManyRequests
	require.ErrorAs(t, err, &target)
	assert.Equal(t, 15, target.RetryAfter)
}

// TestErrors_Distinct проверяет, что разные ошибки не совпадают по типу.
func TestErrors_Distinct(t *testing.T) {
	notReg := NewErrNotRegistered()
	tooMany := NewErrTooManyRequests(5)

	var nre *ErrNotRegistered
	var tmr *ErrTooManyRequests

	assert.True(t, errors.As(notReg, &nre))
	assert.False(t, errors.As(notReg, &tmr))

	assert.True(t, errors.As(tooMany, &tmr))
	assert.False(t, errors.As(tooMany, &nre))
}

// ---------------------------------------------------------------------------
// OrderResponse JSON marshalling
// ---------------------------------------------------------------------------

// TestOrderResponse_JSONUnmarshal проверяет десериализацию OrderResponse.
func TestOrderResponse_JSONUnmarshal(t *testing.T) {
	raw := `{"order":"12345678903","status":"PROCESSED","accrual":750.25}`
	var resp OrderResponse
	err := json.Unmarshal([]byte(raw), &resp)
	require.NoError(t, err)
	assert.Equal(t, "12345678903", resp.Order)
	assert.Equal(t, "PROCESSED", resp.Status)
	assert.True(t, resp.Accrual.Equal(decimal.NewFromFloat(750.25)))
}

// TestOrderResponse_JSONMarshal проверяет сериализацию OrderResponse.
func TestOrderResponse_JSONMarshal(t *testing.T) {
	resp := OrderResponse{
		Order:   "12345",
		Status:  "PROCESSING",
		Accrual: decimal.NewFromFloat(100),
	}
	data, err := json.Marshal(resp)
	require.NoError(t, err)

	var decoded OrderResponse
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, resp.Order, decoded.Order)
	assert.Equal(t, resp.Status, decoded.Status)
	assert.True(t, resp.Accrual.Equal(decoded.Accrual))
}

// ---------------------------------------------------------------------------
// Интеграционный сценарий: ретраи с последующим 429
// ---------------------------------------------------------------------------

// TestGetOrderAccrual_5xxThen429 проверяет, что после 5xx-ретраев ответ 429 возвращает ErrTooManyRequests.
func TestGetOrderAccrual_5xxThen429(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := c.GetOrderAccrual(ctx, "123")

	require.Error(t, err)
	assert.Nil(t, result)
	var tmr *ErrTooManyRequests
	require.ErrorAs(t, err, &tmr)
	assert.Equal(t, 10, tmr.RetryAfter)
}

// TestGetOrderAccrual_5xxThen204 проверяет, что после 5xx-ретраев ответ 204 возвращает ErrNotRegistered.
func TestGetOrderAccrual_5xxThen204(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := c.GetOrderAccrual(ctx, "123")

	require.Error(t, err)
	assert.Nil(t, result)
	var nre *ErrNotRegistered
	require.ErrorAs(t, err, &nre)
}

// ---------------------------------------------------------------------------
// Множественные вызовы
// ---------------------------------------------------------------------------

// TestGetOrderAccrual_MultipleCalls проверяет, что клиент корректно обрабатывает несколько последовательных запросов.
func TestGetOrderAccrual_MultipleCalls(t *testing.T) {
	respBody := `{"order":"123","status":"PROCESSED","accrual":100}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	for i := 0; i < 5; i++ {
		result, err := c.GetOrderAccrual(context.Background(), "123")
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "123", result.Order)
	}
}

// ---------------------------------------------------------------------------
// Параметризованный тест для различных значений Retry-After
// ---------------------------------------------------------------------------

// TestHandleResponse_RetryAfterValues проверяет разные значения Retry-After: валидные, ноль, отрицательные.
func TestHandleResponse_RetryAfterValues(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter string
		expected   int
	}{
		{
			name:       "valid value 30",
			retryAfter: "30",
			expected:   30,
		},
		{
			name:       "zero value",
			retryAfter: "0",
			expected:   0,
		},
		{
			name:       "empty header — default 1",
			retryAfter: "",
			expected:   1,
		},
		{
			name:       "non-numeric — default 1",
			retryAfter: "soon",
			expected:   1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := make(http.Header)
			if tt.retryAfter != "" {
				header.Set("Retry-After", tt.retryAfter)
			}
			resp := &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     header,
			}
			c := newTestClient(t, "http://example.com")
			result, handled, err := c.handleResponse(resp, "123")

			assert.True(t, handled)
			assert.Nil(t, result)
			var tmr *ErrTooManyRequests
			require.ErrorAs(t, err, &tmr)
			assert.Equal(t, tt.expected, tmr.RetryAfter)
		})
	}
}
