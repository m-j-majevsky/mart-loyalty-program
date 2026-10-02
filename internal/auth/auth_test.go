package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// secretKey — общий тестовый секрет для генерации и парсинга токенов.
var secretKey = []byte("test-secret-key")

// TestGenerateUserIDJWT_Success проверяет, что токен генерируется корректно
// и содержит правильные клеймы: UserID и ExpiresAt.
func TestGenerateUserIDJWT_Success(t *testing.T) {
	userID := "123"
	ttl := 1 * time.Hour

	tokenStr, err := GenerateUserIDJWT(userID, ttl, secretKey)
	require.NoError(t, err)
	require.NotEmpty(t, tokenStr)

	// Парсим токен обратно и проверяем клеймы.
	claims := &Claims{}
	token, _, err := jwt.NewParser().ParseUnverified(tokenStr, claims)
	require.NoError(t, err)

	assert.Equal(t, userID, claims.UserID)
	assert.NotNil(t, claims.ExpiresAt)
	assert.WithinDuration(t, time.Now().Add(ttl), claims.ExpiresAt.Time, 5*time.Second)
	assert.Equal(t, jwt.SigningMethodHS256, token.Method)
}

// TestGenerateUserIDJWT_EmptyUserID проверяет генерацию токена
// с пустым userID — клейм просто будет пустым, ошибки нет.
func TestGenerateUserIDJWT_EmptyUserID(t *testing.T) {
	tokenStr, err := GenerateUserIDJWT("", 1*time.Hour, secretKey)
	require.NoError(t, err)
	require.NotEmpty(t, tokenStr)

	uid, err := ParseUserIDJWT(tokenStr, secretKey)
	require.NoError(t, err)
	assert.Equal(t, "", uid)
}

// TestGenerateUserIDJWT_EmptySecret проверяет генерацию токена
// с пустым секретным ключом — должна вернуть ошибку.
func TestGenerateUserIDJWT_EmptySecret(t *testing.T) {
	_, err := GenerateUserIDJWT("123", 1*time.Hour, []byte{})
	assert.Error(t, err)
}

// TestParseUserIDJWT_Success проверяет, что валидный токен
// парсится корректно и возвращает правильный userID.
func TestParseUserIDJWT_Success(t *testing.T) {
	userID := "42"
	tokenStr, err := GenerateUserIDJWT(userID, 1*time.Hour, secretKey)
	require.NoError(t, err)

	parsed, err := ParseUserIDJWT(tokenStr, secretKey)
	require.NoError(t, err)
	assert.Equal(t, userID, parsed)
}

// TestParseUserIDJWT_ExpiredToken проверяет, что просроченный токен
// возвращает ошибку ErrTokenInvalid.
func TestParseUserIDJWT_ExpiredToken(t *testing.T) {
	// Генерируем уже просроченный токен (отрицательный TTL).
	tokenStr, err := GenerateUserIDJWT("42", -1*time.Hour, secretKey)
	require.NoError(t, err)

	_, err = ParseUserIDJWT(tokenStr, secretKey)
	require.Error(t, err)

	var errInvalid *ErrTokenInvalid
	assert.ErrorAs(t, err, &errInvalid)
}

// TestParseUserIDJWT_WrongSecret проверяет, что токен, подписанный
// другим ключом, не проходит верификацию.
func TestParseUserIDJWT_WrongSecret(t *testing.T) {
	tokenStr, err := GenerateUserIDJWT("42", 1*time.Hour, secretKey)
	require.NoError(t, err)

	_, err = ParseUserIDJWT(tokenStr, []byte("wrong-secret"))
	require.Error(t, err)

	var errInvalid *ErrTokenInvalid
	assert.ErrorAs(t, err, &errInvalid)
}

// TestParseUserIDJWT_MalformedToken проверяет, что мусорная строка
// вместо токена возвращает ошибку ErrTokenInvalid.
func TestParseUserIDJWT_MalformedToken(t *testing.T) {
	_, err := ParseUserIDJWT("not-a-jwt", secretKey)
	require.Error(t, err)

	var errInvalid *ErrTokenInvalid
	assert.ErrorAs(t, err, &errInvalid)
}

// TestParseUserIDJWT_EmptyToken проверяет, что пустая строка
// возвращает ошибку ErrTokenInvalid.
func TestParseUserIDJWT_EmptyToken(t *testing.T) {
	_, err := ParseUserIDJWT("", secretKey)
	require.Error(t, err)

	var errInvalid *ErrTokenInvalid
	assert.ErrorAs(t, err, &errInvalid)
}

// TestErrTokenInvalid_Error проверяет текстовое представление ошибки
// с и без внутренней ошибки.
func TestErrTokenInvalid_Error(t *testing.T) {
	// Без внутренней ошибки.
	err := NewErrTokenInvalid(nil)
	assert.Equal(t, "токен невалиден", err.Error())

	// С внутренней ошибкой.
	inner := assert.AnError
	err = NewErrTokenInvalid(inner)
	assert.Contains(t, err.Error(), "токен невалиден")
	assert.Contains(t, err.Error(), inner.Error())
}

// TestErrTokenInvalid_Unwrap проверяет, что errors.Unwrap возвращает
// исходную ошибку.
func TestErrTokenInvalid_Unwrap(t *testing.T) {
	inner := assert.AnError
	err := NewErrTokenInvalid(inner)

	assert.Equal(t, inner, errors.Unwrap(err))
}

// TestErrTokenInvalid_NilInner проверяет, что errors.Unwrap возвращает nil,
// если внутренней ошибки нет.
func TestErrTokenInvalid_NilInner(t *testing.T) {
	err := NewErrTokenInvalid(nil)
	assert.Nil(t, errors.Unwrap(err))
}

// TestGetSecureKeyFunc_HMAC проверяет, что getSecureKeyFunc
// возвращает секрет для HMAC-методов.
func TestGetSecureKeyFunc_HMAC(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{})
	fn := getSecureKeyFunc(secretKey)
	key, err := fn(token)
	require.NoError(t, err)
	assert.Equal(t, secretKey, key)
}

// TestGetSecureKeyFunc_NonHMAC проверяет, что getSecureKeyFunc
// отклоняет неподдерживаемые методы подписания (RSA).
func TestGetSecureKeyFunc_NonHMAC(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{})
	fn := getSecureKeyFunc(secretKey)
	_, err := fn(token)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "недопустимый метод подписания")
}

// TestGenerateAndParseRoundtrip — table-driven тест, проверяющий
// корректность roundtrip генерация→парсинг для разных userID.
func TestGenerateAndParseRoundtrip(t *testing.T) {
	tests := []struct {
		name   string
		userID string
		ttl    time.Duration
	}{
		{"short id", "1", 1 * time.Hour},
		{"long id", "999999999999", 30 * time.Minute},
		{"uuid-like", "550e8400-e29b-41d4-a716-446655440000", 24 * time.Hour},
		{"with special chars", "user_123", 1 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokenStr, err := GenerateUserIDJWT(tt.userID, tt.ttl, secretKey)
			require.NoError(t, err)
			require.NotEmpty(t, tokenStr)

			parsed, err := ParseUserIDJWT(tokenStr, secretKey)
			require.NoError(t, err)
			assert.Equal(t, tt.userID, parsed)
		})
	}
}
