package logger

import (
	"net/http"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Initialize создаёт логер с заданным текстовым уровнем
// логирования ("debug", "info", "warn", "error" и т.д.).
// Возвращает ошибку, если уровень не распознан или не удалось создать логер.
func Initialize(level string) (*zap.Logger, error) {
	lvl, err := zap.ParseAtomicLevel(level)
	if err != nil {
		return nil, err
	}

	cfg := zap.NewProductionConfig()
	cfg.Level = lvl
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	return cfg.Build()
}

// responseData хранит сведения об ответе сервера для логирования.
type responseData struct {
	status int
	size   int
}

// loggingResponseWriter реализует http.ResponseWriter и перехватывает
// код статуса и размер ответа для последующего логирования.
type loggingResponseWriter struct {
	http.ResponseWriter
	responseData *responseData
}

func (r *loggingResponseWriter) Write(b []byte) (int, error) {
	size, err := r.ResponseWriter.Write(b)
	r.responseData.size += size
	return size, err
}

func (r *loggingResponseWriter) WriteHeader(statusCode int) {
	r.ResponseWriter.WriteHeader(statusCode)
	r.responseData.status = statusCode
}

// WithLogging — middleware, логирующая входящие HTTP-запросы и ответы сервера.
// Для каждого запроса выводит URI, метод, длительность обработки,
// а также код статуса и размер ответа.
func WithLogging(log *zap.Logger) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		logFn := func(w http.ResponseWriter, r *http.Request) {
			responseData := &responseData{
				status: 0,
				size:   0,
			}
			lw := loggingResponseWriter{
				ResponseWriter: w,
				responseData:   responseData,
			}

			start := time.Now()
			h.ServeHTTP(&lw, r)
			duration := time.Since(start)

			log.Info("HTTP-запрос",
				zap.String("uri", r.RequestURI),
				zap.String("метод", r.Method),
				zap.Int("статус", responseData.status),
				zap.Int("размер", responseData.size),
				zap.Duration("длительность", duration),
			)
		}
		return http.HandlerFunc(logFn)
	}
}
