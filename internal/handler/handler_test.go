package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/m-j-majevsky/gophermart/internal/auth"
	"github.com/m-j-majevsky/gophermart/internal/config"
	"github.com/m-j-majevsky/gophermart/internal/handler/mocks"
	"github.com/m-j-majevsky/gophermart/internal/service"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	mock "github.com/stretchr/testify/mock"
)

// -----------------------------------------------------------------------------
// Test helpers
// -----------------------------------------------------------------------------

const (
	testCookieName = "gophermart_auth"
	testSigningKey = "test-signing-key"
	testUserID     = int64(42)
	testUserIDStr  = "42"
)

// newTestRouter создаёт Router с mock-сервисом для тестирования хендлеров.
func newTestRouter(t *testing.T) (*Router, *mocks.MockGopherMartService) {
	t.Helper()
	svc := mocks.NewMockGopherMartService(t)
	params := RouterParams{
		Service:    svc,
		Logger:     zap.NewNop(),
		CookieName: testCookieName,
		CookieTTL:  1 * time.Hour,
		SigningKey: []byte(testSigningKey),
	}
	rt := NewRouter(params)
	return rt, svc
}

// authCookie создаёт валидный JWT-cookie для тестов защищённых эндпоинтов.
func authCookie(t *testing.T) *http.Cookie {
	t.Helper()
	token, err := auth.GenerateUserIDJWT(testUserIDStr, 1*time.Hour, []byte(testSigningKey))
	require.NoError(t, err)
	return &http.Cookie{Name: testCookieName, Value: token}
}

// doRequest отправляет запрос в роутер и возвращает ResponseRecorder.
func doRequest(rt *Router, method, path string, body io.Reader, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	return rr
}

// doAuthRequest отправляет аутентифицированный запрос (с валидным JWT-cookie).
func doAuthRequest(t *testing.T, rt *Router, method, path string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	return doRequest(rt, method, path, body, authCookie(t))
}

// jsonBody возвращает io.Reader из JSON-структуры.
func jsonBody(t *testing.T, v interface{}) io.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return bytes.NewReader(b)
}

// -----------------------------------------------------------------------------
// jsonNumber tests
// -----------------------------------------------------------------------------

// TestJSONNumber_MarshalJSON проверяет, что jsonNumber сериализуется как число без кавычек.
func TestJSONNumber_MarshalJSON(t *testing.T) {
	n := jsonNumber(decimal.NewFromFloat(123.45))
	b, err := n.MarshalJSON()
	require.NoError(t, err)
	assert.Equal(t, "123.45", string(b))
}

// TestJSONNumber_MarshalJSON_Zero проверяет сериализацию нулевого значения.
func TestJSONNumber_MarshalJSON_Zero(t *testing.T) {
	n := jsonNumber(decimal.Zero)
	b, err := n.MarshalJSON()
	require.NoError(t, err)
	assert.Equal(t, "0", string(b))
}

// TestJSONNumber_MarshalJSON_Integer проверяет сериализацию целого числа.
func TestJSONNumber_MarshalJSON_Integer(t *testing.T) {
	n := jsonNumber(decimal.NewFromInt(500))
	b, err := n.MarshalJSON()
	require.NoError(t, err)
	assert.Equal(t, "500", string(b))
}

// -----------------------------------------------------------------------------
// NewRouter tests
// -----------------------------------------------------------------------------

// TestNewRouter_NilLogger проверяет, что nil-логгер заменяется на no-op.
func TestNewRouter_NilLogger(t *testing.T) {
	svc := mocks.NewMockGopherMartService(t)
	params := RouterParams{
		Service:    svc,
		Logger:     nil,
		CookieName: testCookieName,
		CookieTTL:  1 * time.Hour,
		SigningKey: []byte(testSigningKey),
	}
	rt := NewRouter(params)
	require.NotNil(t, rt)
	// Логгер должен быть no-op — отправляем запрос к /ping
	svc.EXPECT().Ping(mock.Anything).Return(nil)
	rr := doRequest(rt, http.MethodGet, "/ping", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestNewRouterParams проверяет создание RouterParams из конфигурации.
func TestNewRouterParams(t *testing.T) {
	svc := mocks.NewMockGopherMartService(t)
	cfg := config.ApplicationConfig{
		CookieAuthName: "test_cookie",
		CookieAuthTTL:  2 * time.Hour,
		SigningKey:     []byte("secret"),
	}
	params := NewRouterParams(cfg, svc, zap.NewNop())
	assert.Equal(t, "test_cookie", params.CookieName)
	assert.Equal(t, 2*time.Hour, params.CookieTTL)
	assert.Equal(t, []byte("secret"), params.SigningKey)
	assert.NotNil(t, params.Service)
}

// -----------------------------------------------------------------------------
// getUserIDInt64 tests
// -----------------------------------------------------------------------------

// TestGetUserIDInt64_Success проверяет извлечение корректного userID из контекста.
func TestGetUserIDInt64_Success(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := context.WithValue(req.Context(), userIDKey, "42")
	req = req.WithContext(ctx)
	id, err := getUserIDInt64(req)
	require.NoError(t, err)
	assert.Equal(t, int64(42), id)
}

// TestGetUserIDInt64_NotFound проверяет ошибку при отсутствии userID в контексте.
func TestGetUserIDInt64_NotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err := getUserIDInt64(req)
	assert.Error(t, err)
}

// TestGetUserIDInt64_BadValue проверяет ошибку при некорректном значении userID.
func TestGetUserIDInt64_BadValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := context.WithValue(req.Context(), userIDKey, "not-a-number")
	req = req.WithContext(ctx)
	_, err := getUserIDInt64(req)
	assert.Error(t, err)
}

// -----------------------------------------------------------------------------
// register handler tests
// -----------------------------------------------------------------------------

// TestRegister_Success проверяет успешную регистрацию: 200 + cookie.
func TestRegister_Success(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().RegisterUser(mock.Anything, "alice", "secret").Return(int64(1), nil)
	body := jsonBody(t, map[string]string{"login": "alice", "password": "secret"})
	rr := doRequest(rt, http.MethodPost, "/api/user/register", body)
	assert.Equal(t, http.StatusOK, rr.Code)
	cookies := rr.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, testCookieName, cookies[0].Name)
	assert.NotEmpty(t, cookies[0].Value)
}

// TestRegister_Conflict проверяет, что занятый логин возвращает 409.
func TestRegister_Conflict(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().RegisterUser(mock.Anything, "bob", "pw").Return(int64(0), service.ErrLoginTaken)
	body := jsonBody(t, map[string]string{"login": "bob", "password": "pw"})
	rr := doRequest(rt, http.MethodPost, "/api/user/register", body)
	assert.Equal(t, http.StatusConflict, rr.Code)
}

// TestRegister_InternalError проверяет, что ошибка сервиса возвращает 500.
func TestRegister_InternalError(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().RegisterUser(mock.Anything, "carol", "pw").Return(int64(0), assert.AnError)
	body := jsonBody(t, map[string]string{"login": "carol", "password": "pw"})
	rr := doRequest(rt, http.MethodPost, "/api/user/register", body)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// TestRegister_BadJSON проверяет, что невалидный JSON возвращает 400.
func TestRegister_BadJSON(t *testing.T) {
	rt, _ := newTestRouter(t)
	rr := doRequest(rt, http.MethodPost, "/api/user/register", strings.NewReader("{bad json"))
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// TestRegister_EmptyFields проверяет, что пустые login/password возвращают 400.
func TestRegister_EmptyFields(t *testing.T) {
	rt, _ := newTestRouter(t)
	body := jsonBody(t, map[string]string{"login": "", "password": ""})
	rr := doRequest(rt, http.MethodPost, "/api/user/register", body)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// -----------------------------------------------------------------------------
// login handler tests
// -----------------------------------------------------------------------------

// TestLogin_Success проверяет успешную аутентификацию: 200 + cookie.
func TestLogin_Success(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().AuthenticateUser(mock.Anything, "alice", "secret").Return(int64(1), nil)
	body := jsonBody(t, map[string]string{"login": "alice", "password": "secret"})
	rr := doRequest(rt, http.MethodPost, "/api/user/login", body)
	assert.Equal(t, http.StatusOK, rr.Code)
	cookies := rr.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, testCookieName, cookies[0].Name)
}

// TestLogin_Unauthorized проверяет, что неверные данные возвращают 401.
func TestLogin_Unauthorized(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().AuthenticateUser(mock.Anything, "alice", "wrong").Return(int64(0), service.ErrInvalidCredentials)
	body := jsonBody(t, map[string]string{"login": "alice", "password": "wrong"})
	rr := doRequest(rt, http.MethodPost, "/api/user/login", body)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// TestLogin_InternalError проверяет, что внутренняя ошибка возвращает 500.
func TestLogin_InternalError(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().AuthenticateUser(mock.Anything, "alice", "pw").Return(int64(0), assert.AnError)
	body := jsonBody(t, map[string]string{"login": "alice", "password": "pw"})
	rr := doRequest(rt, http.MethodPost, "/api/user/login", body)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// TestLogin_BadJSON проверяет, что невалидный JSON возвращает 400.
func TestLogin_BadJSON(t *testing.T) {
	rt, _ := newTestRouter(t)
	rr := doRequest(rt, http.MethodPost, "/api/user/login", strings.NewReader("not json"))
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// TestLogin_EmptyFields проверяет, что пустые поля возвращают 400.
func TestLogin_EmptyFields(t *testing.T) {
	rt, _ := newTestRouter(t)
	body := jsonBody(t, map[string]string{"login": "", "password": "x"})
	rr := doRequest(rt, http.MethodPost, "/api/user/login", body)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// -----------------------------------------------------------------------------
// uploadOrder handler tests
// -----------------------------------------------------------------------------

// TestUploadOrder_New проверяет, что новый заказ возвращает 202.
func TestUploadOrder_New(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().UploadOrder(mock.Anything, "79927398713", testUserID).Return(nil)
	rr := doAuthRequest(t, rt, http.MethodPost, "/api/user/orders", strings.NewReader("79927398713"))
	assert.Equal(t, http.StatusAccepted, rr.Code)
}

// TestUploadOrder_DuplicateSameUser проверяет, что повторная загрузка тем же пользователем возвращает 200.
func TestUploadOrder_DuplicateSameUser(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().UploadOrder(mock.Anything, "79927398713", testUserID).Return(service.ErrOrderAlreadyExists)
	svc.EXPECT().GetOrderByNumber(mock.Anything, "79927398713").Return(testUserID, nil)
	rr := doAuthRequest(t, rt, http.MethodPost, "/api/user/orders", strings.NewReader("79927398713"))
	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestUploadOrder_DuplicateOtherUser проверяет, что загрузка заказа другого пользователя возвращает 409.
func TestUploadOrder_DuplicateOtherUser(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().UploadOrder(mock.Anything, "79927398713", testUserID).Return(service.ErrOrderAlreadyExists)
	svc.EXPECT().GetOrderByNumber(mock.Anything, "79927398713").Return(int64(99), nil)
	rr := doAuthRequest(t, rt, http.MethodPost, "/api/user/orders", strings.NewReader("79927398713"))
	assert.Equal(t, http.StatusConflict, rr.Code)
}

// TestUploadOrder_DuplicateGetOwnerError проверяет, что ошибка определения владельца возвращает 500.
func TestUploadOrder_DuplicateGetOwnerError(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().UploadOrder(mock.Anything, "79927398713", testUserID).Return(service.ErrOrderAlreadyExists)
	svc.EXPECT().GetOrderByNumber(mock.Anything, "79927398713").Return(int64(0), assert.AnError)
	rr := doAuthRequest(t, rt, http.MethodPost, "/api/user/orders", strings.NewReader("79927398713"))
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// TestUploadOrder_BadLuhn проверяет, что невалидный номер Луна возвращает 422.
func TestUploadOrder_BadLuhn(t *testing.T) {
	rt, _ := newTestRouter(t)
	rr := doAuthRequest(t, rt, http.MethodPost, "/api/user/orders", strings.NewReader("123"))
	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
}

// TestUploadOrder_EmptyBody проверяет, что пустое тело возвращает 400.
func TestUploadOrder_EmptyBody(t *testing.T) {
	rt, _ := newTestRouter(t)
	rr := doAuthRequest(t, rt, http.MethodPost, "/api/user/orders", strings.NewReader(""))
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// TestUploadOrder_InternalError проверяет, что внутренняя ошибка возвращает 500.
func TestUploadOrder_InternalError(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().UploadOrder(mock.Anything, "79927398713", testUserID).Return(assert.AnError)
	rr := doAuthRequest(t, rt, http.MethodPost, "/api/user/orders", strings.NewReader("79927398713"))
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// TestUploadOrder_Unauthorized проверяет, что запрос без аутентификации возвращает 401.
func TestUploadOrder_Unauthorized(t *testing.T) {
	rt, _ := newTestRouter(t)
	rr := doRequest(rt, http.MethodPost, "/api/user/orders", strings.NewReader("79927398713"))
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// TestUploadOrder_TrailingWhitespace проверяет, что пробельные символы обрезаются перед проверкой Луна.
func TestUploadOrder_TrailingWhitespace(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().UploadOrder(mock.Anything, "79927398713", testUserID).Return(nil)
	rr := doAuthRequest(t, rt, http.MethodPost, "/api/user/orders", strings.NewReader("  79927398713\n"))
	assert.Equal(t, http.StatusAccepted, rr.Code)
}

// -----------------------------------------------------------------------------
// listOrders handler tests
// -----------------------------------------------------------------------------

// TestListOrders_Success проверяет, что список заказов возвращается с 200.
func TestListOrders_Success(t *testing.T) {
	rt, svc := newTestRouter(t)
	now := time.Now()
	orders := []service.OrderDTO{
		{Number: "79927398713", Status: "PROCESSED", Accrual: decimal.NewFromFloat(100.5), UploadedAt: now},
		{Number: "12345678903", Status: "NEW", Accrual: decimal.Zero, UploadedAt: now},
	}
	svc.EXPECT().ListUserOrders(mock.Anything, testUserID).Return(orders, nil)
	rr := doAuthRequest(t, rt, http.MethodGet, "/api/user/orders", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, AppJSON, rr.Header().Get(ContentType))

	var items []map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &items))
	require.Len(t, items, 2)
	assert.Equal(t, "79927398713", items[0]["number"])
	assert.Equal(t, "PROCESSED", items[0]["status"])
	assert.Equal(t, float64(100.5), items[0]["accrual"])
	assert.Nil(t, items[1]["accrual"])
}

// TestListOrders_NoContent проверяет, что пустой список возвращает 204.
func TestListOrders_NoContent(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().ListUserOrders(mock.Anything, testUserID).Return(nil, nil)
	rr := doAuthRequest(t, rt, http.MethodGet, "/api/user/orders", nil)
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

// TestListOrders_InternalError проверяет, что ошибка сервиса возвращает 500.
func TestListOrders_InternalError(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().ListUserOrders(mock.Anything, testUserID).Return(nil, assert.AnError)
	rr := doAuthRequest(t, rt, http.MethodGet, "/api/user/orders", nil)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// TestListOrders_Unauthorized проверяет, что запрос без аутентификации возвращает 401.
func TestListOrders_Unauthorized(t *testing.T) {
	rt, _ := newTestRouter(t)
	rr := doRequest(rt, http.MethodGet, "/api/user/orders", nil)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// -----------------------------------------------------------------------------
// getBalance handler tests
// -----------------------------------------------------------------------------

// TestGetBalance_Success проверяет, что баланс возвращается с 200 и корректным JSON.
func TestGetBalance_Success(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().GetBalance(mock.Anything, testUserID).Return(
		decimal.NewFromFloat(500.5), decimal.NewFromFloat(100.25), nil)
	rr := doAuthRequest(t, rt, http.MethodGet, "/api/user/balance", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, AppJSON, rr.Header().Get(ContentType))

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, float64(500.5), resp["current"])
	assert.Equal(t, float64(100.25), resp["withdrawn"])
}

// TestGetBalance_InternalError проверяет, что ошибка сервиса возвращает 500.
func TestGetBalance_InternalError(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().GetBalance(mock.Anything, testUserID).Return(decimal.Zero, decimal.Zero, assert.AnError)
	rr := doAuthRequest(t, rt, http.MethodGet, "/api/user/balance", nil)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// TestGetBalance_Unauthorized проверяет, что запрос без аутентификации возвращает 401.
func TestGetBalance_Unauthorized(t *testing.T) {
	rt, _ := newTestRouter(t)
	rr := doRequest(rt, http.MethodGet, "/api/user/balance", nil)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// -----------------------------------------------------------------------------
// withdraw handler tests
// -----------------------------------------------------------------------------

// TestWithdraw_Success проверяет, что успешное списание возвращает 200.
func TestWithdraw_Success(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().WithdrawPoints(mock.Anything, testUserID, "79927398713", decimal.NewFromInt(50)).Return(nil)
	body := jsonBody(t, map[string]interface{}{"order": "79927398713", "sum": 50.0})
	rr := doAuthRequest(t, rt, http.MethodPost, "/api/user/balance/withdraw", body)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestWithdraw_InsufficientFunds проверяет, что нехватка средств возвращает 402.
func TestWithdraw_InsufficientFunds(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().WithdrawPoints(mock.Anything, testUserID, "79927398713", decimal.NewFromInt(50)).Return(service.ErrInsufficientFunds)
	body := jsonBody(t, map[string]interface{}{"order": "79927398713", "sum": 50.0})
	rr := doAuthRequest(t, rt, http.MethodPost, "/api/user/balance/withdraw", body)
	assert.Equal(t, http.StatusPaymentRequired, rr.Code)
}

// TestWithdraw_OrderExists проверяет, что дубликат заказа возвращает 422.
func TestWithdraw_OrderExists(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().WithdrawPoints(mock.Anything, testUserID, "79927398713", decimal.NewFromInt(50)).Return(service.ErrOrderAlreadyExists)
	body := jsonBody(t, map[string]interface{}{"order": "79927398713", "sum": 50.0})
	rr := doAuthRequest(t, rt, http.MethodPost, "/api/user/balance/withdraw", body)
	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
}

// TestWithdraw_InternalError проверяет, что внутренняя ошибка возвращает 500.
func TestWithdraw_InternalError(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().WithdrawPoints(mock.Anything, testUserID, "79927398713", decimal.NewFromInt(50)).Return(assert.AnError)
	body := jsonBody(t, map[string]interface{}{"order": "79927398713", "sum": 50.0})
	rr := doAuthRequest(t, rt, http.MethodPost, "/api/user/balance/withdraw", body)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// TestWithdraw_BadLuhn проверяет, что невалидный номер Луна возвращает 422.
func TestWithdraw_BadLuhn(t *testing.T) {
	rt, _ := newTestRouter(t)
	body := jsonBody(t, map[string]interface{}{"order": "123", "sum": 50.0})
	rr := doAuthRequest(t, rt, http.MethodPost, "/api/user/balance/withdraw", body)
	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
}

// TestWithdraw_BadJSON проверяет, что невалидный JSON возвращает 400.
func TestWithdraw_BadJSON(t *testing.T) {
	rt, _ := newTestRouter(t)
	rr := doAuthRequest(t, rt, http.MethodPost, "/api/user/balance/withdraw", strings.NewReader("{bad"))
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// TestWithdraw_EmptyOrder проверяет, что пустой номер заказа возвращает 400.
func TestWithdraw_EmptyOrder(t *testing.T) {
	rt, _ := newTestRouter(t)
	body := jsonBody(t, map[string]interface{}{"order": "", "sum": 50.0})
	rr := doAuthRequest(t, rt, http.MethodPost, "/api/user/balance/withdraw", body)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// TestWithdraw_NonPositiveSum проверяет, что нулевая или отрицательная сумма возвращает 400.
func TestWithdraw_NonPositiveSum(t *testing.T) {
	rt, _ := newTestRouter(t)
	body := jsonBody(t, map[string]interface{}{"order": "79927398713", "sum": 0.0})
	rr := doAuthRequest(t, rt, http.MethodPost, "/api/user/balance/withdraw", body)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// TestWithdraw_Unauthorized проверяет, что запрос без аутентификации возвращает 401.
func TestWithdraw_Unauthorized(t *testing.T) {
	rt, _ := newTestRouter(t)
	body := jsonBody(t, map[string]interface{}{"order": "79927398713", "sum": 50.0})
	rr := doRequest(rt, http.MethodPost, "/api/user/balance/withdraw", body)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// -----------------------------------------------------------------------------
// listWithdrawals handler tests
// -----------------------------------------------------------------------------

// TestListWithdrawals_Success проверяет, что список списаний возвращается с 200.
func TestListWithdrawals_Success(t *testing.T) {
	rt, svc := newTestRouter(t)
	now := time.Now()
	withdrawals := []service.WithdrawalDTO{
		{OrderNumber: "79927398713", Sum: decimal.NewFromFloat(50.0), ProcessedAt: now},
	}
	svc.EXPECT().ListWithdrawals(mock.Anything, testUserID).Return(withdrawals, nil)
	rr := doAuthRequest(t, rt, http.MethodGet, "/api/user/withdrawals", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, AppJSON, rr.Header().Get(ContentType))

	var items []map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &items))
	require.Len(t, items, 1)
	assert.Equal(t, "79927398713", items[0]["order"])
	assert.Equal(t, float64(50.0), items[0]["sum"])
}

// TestListWithdrawals_NoContent проверяет, что пустой список возвращает 204.
func TestListWithdrawals_NoContent(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().ListWithdrawals(mock.Anything, testUserID).Return(nil, nil)
	rr := doAuthRequest(t, rt, http.MethodGet, "/api/user/withdrawals", nil)
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

// TestListWithdrawals_InternalError проверяет, что ошибка сервиса возвращает 500.
func TestListWithdrawals_InternalError(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().ListWithdrawals(mock.Anything, testUserID).Return(nil, assert.AnError)
	rr := doAuthRequest(t, rt, http.MethodGet, "/api/user/withdrawals", nil)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// TestListWithdrawals_Unauthorized проверяет, что запрос без аутентификации возвращает 401.
func TestListWithdrawals_Unauthorized(t *testing.T) {
	rt, _ := newTestRouter(t)
	rr := doRequest(rt, http.MethodGet, "/api/user/withdrawals", nil)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// -----------------------------------------------------------------------------
// pingDB handler tests
// -----------------------------------------------------------------------------

// TestPingDB_Success проверяет, что доступная БД возвращает 200.
func TestPingDB_Success(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().Ping(mock.Anything).Return(nil)
	rr := doRequest(rt, http.MethodGet, "/ping", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestPingDB_Error проверяет, что недоступная БД возвращает 500.
func TestPingDB_Error(t *testing.T) {
	rt, svc := newTestRouter(t)
	svc.EXPECT().Ping(mock.Anything).Return(assert.AnError)
	rr := doRequest(rt, http.MethodGet, "/ping", nil)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}
