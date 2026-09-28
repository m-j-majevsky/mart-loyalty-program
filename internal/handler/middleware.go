package handler

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/m-j-majevsky/gophermart/internal/auth"
)

// Константы Content-Type
const (
	ContentType = "Content-Type"
	AppJSON     = "application/json"
	TextPlain   = "text/plain"
)

// contextKey — тип для ключей контекста запроса.
type contextKey string

// userIDKey — ключ, под которым ID пользователя хранится в контексте запроса.
const userIDKey contextKey = "user_id"

// compressWriter реализует http.ResponseWriter и прозрачно сжимает данные,
// отправляемые клиенту, если клиент поддерживает gzip.
type compressWriter struct {
	w           http.ResponseWriter
	zw          *gzip.Writer
	wroteHeader bool
}

// newCompressWriter создаёт compressWriter, оборачивающий исходный ResponseWriter.
func newCompressWriter(w http.ResponseWriter) *compressWriter {
	return &compressWriter{
		w:  w,
		zw: gzip.NewWriter(w),
	}
}

func (c *compressWriter) Header() http.Header {
	return c.w.Header()
}

func (c *compressWriter) Write(p []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	return c.zw.Write(p)
}

// WriteHeader устанавливает заголовок Content-Encoding: gzip для всех
// статус-кодов, кроме 204 (No Content) и 304 (Not Modified),
// у которых нет тела ответа.
func (c *compressWriter) WriteHeader(statusCode int) {
	if c.wroteHeader {
		return
	}
	c.wroteHeader = true

	if statusCode != http.StatusNoContent && statusCode != http.StatusNotModified {
		c.w.Header().Set("Content-Encoding", "gzip")
	}

	c.w.WriteHeader(statusCode)
}

// Close закрывает gzip.Writer и досылает все данные из буфера.
func (c *compressWriter) Close() error {
	return c.zw.Close()
}

// compressReader реализует io.ReadCloser и прозрачно декомпрессирует
// gzip-сжатое тело запроса.
type compressReader struct {
	r  io.ReadCloser
	zr *gzip.Reader
}

// newCompressReader создаёт compressReader, оборачивающий исходное тело запроса.
func newCompressReader(r io.ReadCloser) (*compressReader, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}

	return &compressReader{
		r:  r,
		zr: zr,
	}, nil
}

func (c *compressReader) Read(p []byte) (n int, err error) {
	return c.zr.Read(p)
}

func (c *compressReader) Close() error {
	if err := c.r.Close(); err != nil {
		return err
	}
	return c.zr.Close()
}

// GzipMiddleware — middleware для прозрачной gzip-компрессии ответов
// и декомпрессии тел запросов.
//
// Логика разделена на два независимых механизма:
//   - декомпрессия тела запроса — если заголовок Content-Encoding содержит gzip;
//   - компрессия ответа — если заголовок Accept-Encoding содержит gzip.
func GzipMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Декомпрессия тела запроса, если клиент прислал gzip.
		contentEncoding := r.Header.Get("Content-Encoding")
		if strings.Contains(contentEncoding, "gzip") {
			cr, err := newCompressReader(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			r.Body = cr
			defer cr.Close()
		}

		// Компрессия ответа, если клиент поддерживает gzip.
		ow := w
		acceptEncoding := r.Header.Get("Accept-Encoding")
		if strings.Contains(acceptEncoding, "gzip") {
			cw := newCompressWriter(w)
			ow = cw
			defer cw.Close()
		}

		h.ServeHTTP(ow, r)
	})
}

// RequireAuth — middleware, проверяющая аутентификацию пользователя
// по JWT-токену в cookie. Если токен валиден, помещает ID пользователя
// в контекст запроса и передаёт управление следующему хендлеру.
// Если токен отсутствует или невалиден, возвращает 401 Unauthorized.
func RequireAuth(signingKey []byte, cookieName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(cookieName)
			if err != nil {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			userID, err := auth.ParseUserIDJWT(cookie.Value, signingKey)
			if err != nil {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			if userID == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), userIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// getUserIDFromContext извлекает ID пользователя из контекста запроса.
// Возвращает ID и nil при успехе, или пустую строку и ошибку, если ключ не найден.
func getUserIDFromContext(ctx context.Context) (string, error) {
	val, ok := ctx.Value(userIDKey).(string)
	if !ok {
		return "", fmt.Errorf("ID пользователя не найден в контексте")
	}
	return val, nil
}

// setAuthCookie устанавливает cookie с JWT-токеном аутентификации
// в HTTP-ответе. Параметры cookieName, ttl и signingKey определяют
// имя cookie, срок действия и ключ подписи соответственно.
func setAuthCookie(w http.ResponseWriter, cookieName string, ttl time.Duration, signingKey []byte, userID string) error {
	token, err := auth.GenerateUserIDJWT(userID, ttl, signingKey)
	if err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(ttl),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})

	return nil
}
