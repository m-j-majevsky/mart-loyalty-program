package repository

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// User представляет запись пользователя в базе данных.
type User struct {
	ID           int64
	Login        string
	PasswordHash string
	Balance      decimal.Decimal
	Withdrawn    decimal.Decimal
}

// Order представляет запись заказа в базе данных.
type Order struct {
	Number     string          `db:"number"`      // номер заказа
	Status     string          `db:"status"`      // внутренний статус: NEW, PROCESSING, INVALID, PROCESSED
	Accrual    decimal.Decimal `db:"accrual"`     // начисленные баллы (для PROCESSED)
	UploadedAt time.Time       `db:"uploaded_at"` // время загрузки заказа в систему
}

// Withdrawal представляет запись о списании баллов.
type Withdrawal struct {
	OrderNumber string          `db:"order_number"` // номер заказа, в счёт которого списаны баллы
	Sum         decimal.Decimal `db:"sum"`          // сумма списания
	ProcessedAt time.Time       `db:"processed_at"` // время списания
}

// OrderUpdate содержит данные для пакетного обновления статуса заказа
// и начисления баллов в базе данных.
type OrderUpdate struct {
	Number  string          // номер заказа
	Status  string          // новый статус заказа
	Accrual decimal.Decimal // начисленные баллы (0, если нет начисления)
}

// Ошибки хранилища

// ErrLoginTaken означает, что логин уже занят другим пользователем.
type ErrLoginTaken struct {
	Login string
}

func NewErrLoginTaken(login string) error {
	return &ErrLoginTaken{Login: login}
}

func (e *ErrLoginTaken) Error() string {
	return fmt.Sprintf("логин %s уже занят", e.Login)
}

// ErrUserNotFound означает, что пользователь с указанным логином не найден.
type ErrUserNotFound struct {
	Login string
}

func NewErrUserNotFound(login string) error {
	return &ErrUserNotFound{Login: login}
}

func (e *ErrUserNotFound) Error() string {
	return fmt.Sprintf("пользователь %s не найден", e.Login)
}

// ErrOrderAlreadyExists означает, что заказ с таким номером уже зарегистрирован.
type ErrOrderAlreadyExists struct {
	Number string
}

func NewErrOrderAlreadyExists(number string) error {
	return &ErrOrderAlreadyExists{Number: number}
}

func (e *ErrOrderAlreadyExists) Error() string {
	return fmt.Sprintf("заказ %s уже зарегистрирован", e.Number)
}

// ErrOrderNotFound означает, что заказ с указанным номером не найден.
type ErrOrderNotFound struct {
	Number string
}

func NewErrOrderNotFound(number string) error {
	return &ErrOrderNotFound{Number: number}
}

func (e *ErrOrderNotFound) Error() string {
	return fmt.Sprintf("заказ %s не найден", e.Number)
}

// ErrInsufficientFunds означает, что на балансе пользователя недостаточно баллов.
type ErrInsufficientFunds struct{}

func NewErrInsufficientFunds() error {
	return &ErrInsufficientFunds{}
}

func (e *ErrInsufficientFunds) Error() string {
	return "недостаточно баллов на счёте"
}
