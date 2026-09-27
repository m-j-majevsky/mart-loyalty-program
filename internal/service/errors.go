package service

import "errors"

// Ошибки сервисного слоя.
// Каждая ошибка — sentinel-значение, используемое хендлером
// для выбора HTTP-кода ответа без импорта пакета repository.

var (
	// ErrLoginTaken — логин уже занят другим пользователем.
	ErrLoginTaken = errors.New("логин уже занят")

	// ErrInvalidCredentials — неверная пара логин/пароль.
	ErrInvalidCredentials = errors.New("неверная пара логин/пароль")

	// ErrInsufficientFunds — недостаточно баллов для списания.
	ErrInsufficientFunds = errors.New("недостаточно баллов на счёте")

	// ErrOrderAlreadyExists — заказ уже зарегистрирован в системе.
	ErrOrderAlreadyExists = errors.New("заказ уже зарегистрирован")
)
