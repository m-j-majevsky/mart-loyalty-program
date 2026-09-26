package service

import "errors"

// Ошибки сервисного слоя

var (
	// ErrLoginTaken — логин уже занят.
	ErrLoginTaken = errors.New("логин уже занят")

	// ErrInvalidCredentials — неверная пара логин/пароль.
	ErrInvalidCredentials = errors.New("неверная пара логин/пароль")

	// ErrInvalidOrderNumber — неверный формат номера заказа.
	ErrInvalidOrderNumber = errors.New("неверный формат номера заказа")

	// ErrInsufficientFunds — недостаточно баллов для списания.
	ErrInsufficientFunds = errors.New("недостаточно баллов на счёте")

	// ErrOrderAlreadyExists — заказ уже зарегистрирован в системе.
	ErrOrderAlreadyExists = errors.New("заказ уже зарегистрирован")

	// ErrOrderOwnedByAnother — заказ загружен другим пользователем.
	ErrOrderOwnedByAnother = errors.New("заказ загружен другим пользователем")
)
