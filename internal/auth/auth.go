package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

// ErrTokenInvalid означает, что JWT-токен невалиден или не может быть разобран.
type ErrTokenInvalid struct {
	Err error
}

// Error возвращает текстовое описание ошибки.
func (e *ErrTokenInvalid) Error() string {
	msg := "токен невалиден"
	if e.Err != nil {
		msg = fmt.Sprintf("%s: %v", msg, e.Err)
	}
	return msg
}

// NewErrTokenInvalid конструирует ошибку ErrTokenInvalid, оборачивая исходную.
func NewErrTokenInvalid(err error) error {
	return &ErrTokenInvalid{Err: err}
}

// Claims описывает структуру JWT-клеймов с пользовательским полем UserID.
type Claims struct {
	jwt.RegisteredClaims
	UserID string `json:"user_id"`
}

// GenerateUserIDJWT создаёт подписанный JWT-токен с userID и сроком действия ttl,
// используя секретный ключ jwtSecret. Возвращает строку токена или ошибку.
func GenerateUserIDJWT(userID string, ttl time.Duration, jwtSecret []byte) (string, error) {
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
		},
		UserID: userID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	tokenString, err := token.SignedString(jwtSecret)
	if err != nil {
		return "", err
	}

	return tokenString, nil
}

// ParseUserIDJWT разбирает JWT-токен из cookieValue, проверяет подпись
// с помощью jwtSecret и возвращает userID, извлечённый из клеймов.
// Если токен невалиден или подпись не совпадает, возвращает ошибку ErrTokenInvalid.
func ParseUserIDJWT(cookieValue string, jwtSecret []byte) (string, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(cookieValue, claims, getSecureKeyFunc(jwtSecret))
	if err != nil {
		return "", NewErrTokenInvalid(err)
	}
	if !token.Valid {
		return "", NewErrTokenInvalid(nil)
	}

	return claims.UserID, nil
}

// getSecureKeyFunc возвращает функцию проверки ключа подписи JWT,
// которая гарантирует использование метода HMAC (HS256).
func getSecureKeyFunc(jwtSecret []byte) jwt.Keyfunc {
	return func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("недопустимый метод подписания: %v", t.Header["alg"])
		}
		return jwtSecret, nil
	}
}
