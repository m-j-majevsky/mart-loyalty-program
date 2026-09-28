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

func (c *compressWriter) WriteHeader(statusCode int) {
	if c.wroteHeader {
		return
	}
	c.wroteHeader = true

	if statusCode < 300 {
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

// GzipMiddleware — middleware для прозрачной компрессии/декомпрессии
// HTTP-запросов и ответов в формате gzip. Если клиент не поддерживает gzip
// или Content-Type не входит в список разрешённых, передаёт запрос дальше без изменений.
func GzipMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isValidRequestContentType(r.Header.Get(ContentType)) {
			h.ServeHTTP(w, r)
			return
		}

		ow := w

		acceptEncoding := r.Header.Get("Accept-Encoding")
		supportsGzip := strings.Contains(acceptEncoding, "gzip")

		if supportsGzip {
			cw := newCompressWriter(w)
			ow = cw
			defer cw.Close()
		}

		contentEncoding := r.Header.Get("Content-Encoding")
		sendsGzip := strings.Contains(contentEncoding, "gzip")
		if sendsGzip {
			cr, err := newCompressReader(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			r.Body = cr
			defer cr.Close()
		}

		h.ServeHTTP(ow, r)
	})
}

// isValidRequestContentType проверяет, что Content-Type запроса
// входит в список разрешённых для gzip-обработки.
func isValidRequestContentType(ct string) bool {
	return ct == TextPlain || ct == AppJSON
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
