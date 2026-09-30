package logger

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// TestInitialize_ValidLevels — table-driven тест, проверяющий, что
// Initialize создаёт логгер для всех поддерживаемых уровней.
func TestInitialize_ValidLevels(t *testing.T) {
	levels := []string{"debug", "info", "warn", "error", "dpanic", "panic", "fatal"}

	for _, lvl := range levels {
		t.Run(lvl, func(t *testing.T) {
			lg, err := Initialize(lvl)
			require.NoError(t, err)
			require.NotNil(t, lg)
			defer lg.Sync()
		})
	}
}

// TestInitialize_InvalidLevel проверяет, что Initialize возвращает ошибку
// при неизвестном уровне логирования.
func TestInitialize_InvalidLevel(t *testing.T) {
	_, err := Initialize("nonexistent-level")
	require.Error(t, err)
}

// TestInitialize_EmptyLevel проверяет, что Initialize с пустой строкой
// уровня создаёт логгер уровня info (zap treats "" as info — zero value).
func TestInitialize_EmptyLevel(t *testing.T) {
	lg, err := Initialize("")
	require.NoError(t, err)
	require.NotNil(t, lg)
	defer lg.Sync()
}

// TestWithLogging_RecordsRequest проверяет, что middleware логирует
// URI, метод, статус и размер ответа при обработке запроса.
func TestWithLogging_RecordsRequest(t *testing.T) {
	// Создаём наблюдаемый логгер для проверки записей.
	core, logs := observer.New(zapcore.DebugLevel)
	lg := zap.New(core)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte("test-response-body"))
	})

	mw := WithLogging(lg)(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/test?foo=bar", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	// Проверяем, что логгер записал одно сообщение.
	require.Equal(t, 1, logs.Len(), "expected exactly 1 log entry")

	entry := logs.All()[0]
	assert.Equal(t, "HTTP-request", entry.Message)

	// Проверяем поля записи лога.
	// Используем EqualValues, так как zap ContextMap() возвращает int64
	// для полей, заданных через zap.Int().
	fields := entry.ContextMap()
	assert.Equal(t, "/api/test?foo=bar", fields["uri"])
	assert.Equal(t, http.MethodGet, fields["method"])
	assert.EqualValues(t, http.StatusAccepted, fields["status"])
	assert.EqualValues(t, len("test-response-body"), fields["size"])
}

// TestWithLogging_RecordsPostRequest проверяет логирование POST-запроса
// с телом и ответом 200.
func TestWithLogging_RecordsPostRequest(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	lg := zap.New(core)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	mw := WithLogging(lg)(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/orders", strings.NewReader(`{"order":"123"}`))
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	require.Equal(t, 1, logs.Len())
	entry := logs.All()[0]
	fields := entry.ContextMap()
	assert.Equal(t, "/api/orders", fields["uri"])
	assert.Equal(t, http.MethodPost, fields["method"])
	assert.EqualValues(t, http.StatusOK, fields["status"])
	assert.EqualValues(t, 2, fields["size"])
}

// TestWithLogging_NoStatus проверяет, что middleware корректно логирует
// запрос, в котором хендлер не вызывает WriteHeader (по умолчанию 200).
func TestWithLogging_NoStatus(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	lg := zap.New(core)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	})

	mw := WithLogging(lg)(handler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	require.Equal(t, 1, logs.Len())
	fields := logs.All()[0].ContextMap()
	// status остаётся 0, так как WriteHeader не вызывался явно.
	assert.EqualValues(t, 0, fields["status"])
	assert.EqualValues(t, 5, fields["size"])
}

// TestLoggingResponseWriter_Write проверяет, что Write корректно
// суммирует размер записанных данных.
func TestLoggingResponseWriter_Write(t *testing.T) {
	rr := httptest.NewRecorder()
	rd := &responseData{status: 0, size: 0}
	lw := loggingResponseWriter{
		ResponseWriter: rr,
		responseData:   rd,
	}

	n, err := lw.Write([]byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, 5, rd.size)

	// Второй вызов должен добавить к существующему размеру.
	n, err = lw.Write([]byte(" world"))
	require.NoError(t, err)
	assert.Equal(t, 6, n)
	assert.Equal(t, 11, rd.size)
}

// TestLoggingResponseWriter_WriteHeader проверяет, что WriteHeader
// сохраняет код статуса.
func TestLoggingResponseWriter_WriteHeader(t *testing.T) {
	rr := httptest.NewRecorder()
	rd := &responseData{status: 0, size: 0}
	lw := loggingResponseWriter{
		ResponseWriter: rr,
		responseData:   rd,
	}

	lw.WriteHeader(http.StatusNotFound)
	assert.Equal(t, http.StatusNotFound, rd.status)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// TestLoggingResponseWriter_Header проверяет, что Header делегирует
// вызов к базовому ResponseWriter.
func TestLoggingResponseWriter_Header(t *testing.T) {
	rr := httptest.NewRecorder()
	rd := &responseData{}
	lw := loggingResponseWriter{
		ResponseWriter: rr,
		responseData:   rd,
	}

	h := lw.Header()
	h.Set("X-Custom", "value")
	assert.Equal(t, "value", rr.Header().Get("X-Custom"))
}
