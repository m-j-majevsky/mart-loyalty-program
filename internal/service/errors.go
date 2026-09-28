package service

import "errors"

// Ошибки сервисного слоя.
// Каждая ошибка — sentinel-значение, используемое хендлером для выбора HTTP-кода ответа.

var (
	ErrLoginTaken         = errors.New("логин уже занят")
	ErrInvalidCredentials = errors.New("неверная пара логин/пароль")
	ErrInsufficientFunds  = errors.New("недостаточно баллов на счёте")
	ErrOrderAlreadyExists = errors.New("заказ уже зарегистрирован")
)
