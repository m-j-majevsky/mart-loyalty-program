package repository

import (
	"context"
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
	Number     string          // номер заказа
	Status     string          // внутренний статус: NEW, PROCESSING, INVALID, PROCESSED
	Accrual    decimal.Decimal // начисленные баллы (для PROCESSED)
	UploadedAt time.Time       // время загрузки заказа в систему
}

// Withdrawal представляет запись о списании баллов.
type Withdrawal struct {
	OrderNumber string          // номер заказа, в счёт которого списаны баллы
	Sum         decimal.Decimal // сумма списания
	ProcessedAt time.Time       // время списания
}

// OrderUpdate содержит данные для пакетного обновления статуса заказа
// и начисления баллов в базе данных.
type OrderUpdate struct {
	Number  string          // номер заказа
	Status  string          // новый статус заказа
	Accrual decimal.Decimal // начисленные баллы (0, если нет начисления)
}

// Storage определяет интерфейс хранилища данных сервиса лояльности.
// Реализация — PgStorage, но интерфейс позволяет подменять хранилище
// в тестах и при развитии сервиса.
type Storage interface {
	// CreateUser регистрирует нового пользователя с указанными логином и хешем пароля.
	// Возвращает ID созданного пользователя или ошибку, если логин уже занят.
	CreateUser(ctx context.Context, login, passwordHash string) (int64, error)

	// GetUserByLogin ищет пользователя по логину.
	// Возвращает структуру User или ошибку, если пользователь не найден.
	GetUserByLogin(ctx context.Context, login string) (User, error)

	// CreateOrder создаёт запись о новом заказе для пользователя userID.
	// Если заказ с таким номером уже существует, возвращает ErrOrderAlreadyExists.
	CreateOrder(ctx context.Context, number string, userID int64) error

	// GetOrderByNumber ищет заказ по номеру и возвращает ID пользователя-владельца.
	// Используется для определения, кем был загружен заказ при обработке дубликата.
	// Возвращает ErrOrderNotFound, если заказ не найден.
	GetOrderByNumber(ctx context.Context, number string) (int64, error)

	// ListUserOrders возвращает все заказы пользователя, отсортированные
	// от самых новых к самым старым по времени загрузки.
	ListUserOrders(ctx context.Context, userID int64) ([]Order, error)

	// GetBalance возвращает текущий баланс и сумму списаний пользователя.
	GetBalance(ctx context.Context, userID int64) (balance, withdrawn decimal.Decimal, err error)

	// WithdrawPoints списывает баллы с баланса пользователя в счёт заказа orderNo.
	// Выполняется в одной транзакции: блокировка пользователя, проверка баланса,
	// списание, создание заказа и записи о списании.
	// Возвращает ErrInsufficientFunds, если баллов недостаточно,
	// или ErrOrderAlreadyExists, если заказ уже зарегистрирован.
	WithdrawPoints(ctx context.Context, userID int64, orderNo string, sum decimal.Decimal) error

	// ListWithdrawals возвращает все списания пользователя, отсортированные
	// от самых новых к самым старым по времени списания.
	ListWithdrawals(ctx context.Context, userID int64) ([]Withdrawal, error)

	// ListPendingOrderNumbers возвращает номера всех заказов в статусах
	// NEW и PROCESSING, которые требуют опроса accrual-системы.
	// Используется при запуске сервиса для восстановления очереди после перезапуска.
	ListPendingOrderNumbers(ctx context.Context) ([]string, error)

	// BatchUpdateOrders пакетно обновляет статусы и начисления для списка заказов.
	// Для каждого заказа со статусом PROCESSED начисляет баллы на баланс пользователя.
	BatchUpdateOrders(ctx context.Context, updates []OrderUpdate) error

	// Ping проверяет доступность хранилища.
	Ping(ctx context.Context) error
}

// Ошибки хранилища

// ErrLoginTaken означает, что логин уже занят другим пользователем.
type ErrLoginTaken struct {
	Login string
}

// NewErrLoginTaken конструирует ошибку ErrLoginTaken.
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

// NewErrUserNotFound конструирует ошибку ErrUserNotFound.
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

// NewErrOrderAlreadyExists конструирует ошибку ErrOrderAlreadyExists.
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

// NewErrOrderNotFound конструирует ошибку ErrOrderNotFound.
func NewErrOrderNotFound(number string) error {
	return &ErrOrderNotFound{Number: number}
}

func (e *ErrOrderNotFound) Error() string {
	return fmt.Sprintf("заказ %s не найден", e.Number)
}

// ErrInsufficientFunds означает, что на балансе пользователя недостаточно баллов.
type ErrInsufficientFunds struct{}

// NewErrInsufficientFunds конструирует ошибку ErrInsufficientFunds.
func NewErrInsufficientFunds() error {
	return &ErrInsufficientFunds{}
}

func (e *ErrInsufficientFunds) Error() string {
	return "недостаточно баллов на счёте"
}
