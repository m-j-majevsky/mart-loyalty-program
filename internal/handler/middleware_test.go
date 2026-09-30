package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/m-j-majevsky/gophermart/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -----------------------------------------------------------------------------
// compressWriter tests
// -----------------------------------------------------------------------------

// TestCompressWriter_Write проверяет, что gzip-сжатый ответ корректно декомпрессируется.
func TestCompressWriter_Write(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := newCompressWriter(rec)
	body := []byte("hello world")
	n, err := cw.Write(body)
	require.NoError(t, err)
	assert.Equal(t, len(body), n)
	require.NoError(t, cw.Close())

	assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	assert.Equal(t, http.StatusOK, rec.Code)

	zr, err := gzip.NewReader(rec.Body)
	require.NoError(t, err)
	decompressed, err := io.ReadAll(zr)
	require.NoError(t, err)
	assert.Equal(t, body, decompressed)
}

// TestCompressWriter_WriteHeader204 проверяет, что статус 204 не получает Content-Encoding: gzip.
func TestCompressWriter_WriteHeader204(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := newCompressWriter(rec)
	cw.WriteHeader(http.StatusNoContent)
	require.NoError(t, cw.Close())

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Header().Get("Content-Encoding"))
	assert.Empty(t, rec.Body.Bytes())
}

// TestCompressWriter_WriteHeader304 проверяет, что статус 304 не получает Content-Encoding: gzip.
func TestCompressWriter_WriteHeader304(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := newCompressWriter(rec)
	cw.WriteHeader(http.StatusNotModified)
	require.NoError(t, cw.Close())

	assert.Equal(t, http.StatusNotModified, rec.Code)
	assert.Empty(t, rec.Header().Get("Content-Encoding"))
}

// TestCompressWriter_DoubleWriteHeader проверяет, что повторный вызов WriteHeader игнорируется.
func TestCompressWriter_DoubleWriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := newCompressWriter(rec)
	cw.WriteHeader(http.StatusOK)
	cw.WriteHeader(http.StatusInternalServerError)
	require.NoError(t, cw.Close())
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestCompressWriter_Header проверяет, что Header() делегирует исходному ResponseWriter.
func TestCompressWriter_Header(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := newCompressWriter(rec)
	cw.Header().Set("X-Custom", "yes")
	require.NoError(t, cw.Close())
	assert.Equal(t, "yes", rec.Header().Get("X-Custom"))
}

// -----------------------------------------------------------------------------
// compressReader tests
// -----------------------------------------------------------------------------

// TestCompressReader_Read проверяет, что gzip-сжатое тело корректно декомпрессируется.
func TestCompressReader_Read(t *testing.T) {
	// Сжимаем данные.
	original := []byte("compressed request body")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(original)
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	cr, err := newCompressReader(io.NopCloser(&buf))
	require.NoError(t, err)
	defer cr.Close()

	decompressed, err := io.ReadAll(cr)
	require.NoError(t, err)
	assert.Equal(t, original, decompressed)
}

// TestCompressReader_InvalidGzip проверяет, что некорректный gzip возвращает ошибку.
func TestCompressReader_InvalidGzip(t *testing.T) {
	_, err := newCompressReader(io.NopCloser(bytes.NewReader([]byte("not gzip data"))))
	assert.Error(t, err)
}

// -----------------------------------------------------------------------------
// GzipMiddleware tests
// -----------------------------------------------------------------------------

// gzipMiddlewareHandler — вспомогательный хендлер, возвращающий текстовый ответ.
func gzipMiddlewareHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Если тело было декомпрессировано, читаем и возвращаем его.
		body, _ := io.ReadAll(r.Body)
		w.Write([]byte("response:" + string(body)))
	})
}

// TestGzipMiddleware_CompressResponse проверяет, что ответ сжимается, если клиент поддерживает gzip.
func TestGzipMiddleware_CompressResponse(t *testing.T) {
	handler := GzipMiddleware(gzipMiddlewareHandler())
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, "gzip", rr.Header().Get("Content-Encoding"))
	zr, err := gzip.NewReader(rr.Body)
	require.NoError(t, err)
	decompressed, err := io.ReadAll(zr)
	require.NoError(t, err)
	assert.Equal(t, "response:", string(decompressed))
}

// TestGzipMiddleware_DecompressRequest проверяет, что gzip-тело запроса декомпрессируется.
// Запрос отправляется с Content-Encoding: gzip, но без Accept-Encoding: gzip,
// поэтому ответ возвращается в plain text (без компрессии).
func TestGzipMiddleware_DecompressRequest(t *testing.T) {
	// Сжимаем тело запроса.
	original := []byte("hello-gzip")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(original)
	zw.Close()

	handler := GzipMiddleware(gzipMiddlewareHandler())
	req := httptest.NewRequest(http.MethodPost, "/", &buf)
	req.Header.Set("Content-Encoding", "gzip")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Ответ не сжат, так как нет Accept-Encoding: gzip — читаем напрямую.
	assert.Empty(t, rr.Header().Get("Content-Encoding"))
	assert.Equal(t, "response:hello-gzip", rr.Body.String())
}

// TestGzipMiddleware_NoGzip проверяет, что без заголовков gzip middleware работает прозрачно.
func TestGzipMiddleware_NoGzip(t *testing.T) {
	handler := GzipMiddleware(gzipMiddlewareHandler())
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("plain"))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Empty(t, rr.Header().Get("Content-Encoding"))
	assert.Equal(t, "response:plain", rr.Body.String())
}

// TestGzipMiddleware_InvalidGzipRequest проверяет, что некорректное gzip-тело возвращает 400.
func TestGzipMiddleware_InvalidGzipRequest(t *testing.T) {
	handler := GzipMiddleware(gzipMiddlewareHandler())
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not gzip"))
	req.Header.Set("Content-Encoding", "gzip")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// -----------------------------------------------------------------------------
// RequireAuth middleware tests
// -----------------------------------------------------------------------------

// TestRequireAuth_ValidCookie проверяет, что валидный токен пропускает запрос.
func TestRequireAuth_ValidCookie(t *testing.T) {
	token, err := auth.GenerateUserIDJWT(testUserIDStr, 1*time.Hour, []byte(testSigningKey))
	require.NoError(t, err)

	called := false
	mw := RequireAuth([]byte(testSigningKey), testCookieName)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		uid, err := getUserIDFromContext(r.Context())
		require.NoError(t, err)
		assert.Equal(t, testUserIDStr, uid)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/user/orders", nil)
	req.AddCookie(&http.Cookie{Name: testCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestRequireAuth_NoCookie проверяет, что отсутствие cookie возвращает 401.
func TestRequireAuth_NoCookie(t *testing.T) {
	mw := RequireAuth([]byte(testSigningKey), testCookieName)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/user/orders", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// TestRequireAuth_InvalidToken проверяет, что невалидный токен возвращает 401.
func TestRequireAuth_InvalidToken(t *testing.T) {
	mw := RequireAuth([]byte(testSigningKey), testCookieName)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/user/orders", nil)
	req.AddCookie(&http.Cookie{Name: testCookieName, Value: "invalid-token"})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// TestRequireAuth_EmptyUserID проверяет, что токен с пустым userID возвращает 401.
func TestRequireAuth_EmptyUserID(t *testing.T) {
	token, err := auth.GenerateUserIDJWT("", 1*time.Hour, []byte(testSigningKey))
	require.NoError(t, err)

	mw := RequireAuth([]byte(testSigningKey), testCookieName)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/user/orders", nil)
	req.AddCookie(&http.Cookie{Name: testCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// TestRequireAuth_WrongKey проверяет, что токен с другим ключом подписи возвращает 401.
func TestRequireAuth_WrongKey(t *testing.T) {
	token, err := auth.GenerateUserIDJWT(testUserIDStr, 1*time.Hour, []byte("other-key"))
	require.NoError(t, err)

	mw := RequireAuth([]byte(testSigningKey), testCookieName)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/user/orders", nil)
	req.AddCookie(&http.Cookie{Name: testCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// -----------------------------------------------------------------------------
// getUserIDFromContext tests
// -----------------------------------------------------------------------------

// TestGetUserIDFromContext_Success проверяет извлечение userID из контекста.
func TestGetUserIDFromContext_Success(t *testing.T) {
	ctx := context.WithValue(context.Background(), userIDKey, "123")
	uid, err := getUserIDFromContext(ctx)
	require.NoError(t, err)
	assert.Equal(t, "123", uid)
}

// TestGetUserIDFromContext_NotFound проверяет ошибку при отсутствии ключа.
func TestGetUserIDFromContext_NotFound(t *testing.T) {
	uid, err := getUserIDFromContext(context.Background())
	assert.Error(t, err)
	assert.Empty(t, uid)
}

// TestGetUserIDFromContext_WrongType проверяет ошибку при неверном типе значения.
func TestGetUserIDFromContext_WrongType(t *testing.T) {
	ctx := context.WithValue(context.Background(), userIDKey, 123)
	uid, err := getUserIDFromContext(ctx)
	assert.Error(t, err)
	assert.Empty(t, uid)
}

// -----------------------------------------------------------------------------
// setAuthCookie tests
// -----------------------------------------------------------------------------

// TestSetAuthCookie_Success проверяет, что cookie устанавливается с валидным JWT.
func TestSetAuthCookie_Success(t *testing.T) {
	rr := httptest.NewRecorder()
	err := setAuthCookie(rr, testCookieName, 1*time.Hour, []byte(testSigningKey), "42")
	require.NoError(t, err)

	cookies := rr.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, testCookieName, cookies[0].Name)
	assert.NotEmpty(t, cookies[0].Value)
	assert.True(t, cookies[0].HttpOnly)

	// Проверяем, что токен валиден.
	uid, err := auth.ParseUserIDJWT(cookies[0].Value, []byte(testSigningKey))
	require.NoError(t, err)
	assert.Equal(t, "42", uid)
}
