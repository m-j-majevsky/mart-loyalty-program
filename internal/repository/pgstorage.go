package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"
)

// dbtx — минимальный набор методов для работы с БД.
// Реализуется *pgxpool.Pool, что позволяет покрывать репозиторий
// unit-тестами через мок, реализующий тот же интерфейс.
// Интерфейс приватный, чтобы не просачивать типы pgx наружу пакета.
type dbtx interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Ping(ctx context.Context) error
	Begin(ctx context.Context) (pgx.Tx, error)
}

// pgStorage — реализация интерфейсов хранилища (service.UserStore, OrderStore, WithdrawalStore, Pinger).
type pgStorage struct {
	db dbtx
}

// NewPgStorage создаёт экземпляр pgStorage на основе переданного пула соединений
// (или любого другого объекта, реализующего интерфейс dbtx).
func NewPgStorage(db dbtx) *pgStorage {
	return &pgStorage{db: db}
}

// Имена проверяемых ограничений слоя PostgreSQL.
const (
	constraintUsersLoginKey   = "users_login_key"
	constraintOrdersNumberKey = "orders_number_key"
)

// CreateUser регистрирует нового пользователя с указанными логином и хешем пароля.
// Возвращает ID созданного пользователя.
// Если логин уже занят, возвращает ErrLoginTaken.
func (s *pgStorage) CreateUser(ctx context.Context, login, passwordHash string) (int64, error) {
	const q = `INSERT INTO users (login, password_hash) 
	           VALUES ($1, $2) 
			   RETURNING id`

	var id int64
	err := s.db.QueryRow(ctx, q, login, passwordHash).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation &&
			pgErr.ConstraintName == constraintUsersLoginKey {
			return 0, NewErrLoginTaken(login)
		}
		return 0, fmt.Errorf("ошибка создания пользователя: %w", err)
	}

	return id, nil
}

// GetUserByLogin ищет пользователя по логину.
// Возвращает структуру User или ErrUserNotFound, если пользователь не найден.
func (s *pgStorage) GetUserByLogin(ctx context.Context, login string) (User, error) {
	const q = `SELECT id, login, password_hash, balance, withdrawn 
	           FROM users 
			   WHERE login = $1`

	var u User
	err := s.db.QueryRow(ctx, q, login).Scan(&u.ID, &u.Login, &u.PasswordHash, &u.Balance, &u.Withdrawn)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, NewErrUserNotFound(login)
	}
	if err != nil {
		return User{}, fmt.Errorf("ошибка запроса пользователя по логину %q: %w", login, err)
	}

	return u, nil
}

// CreateOrder создаёт запись о новом заказе для пользователя userID.
// Если заказ с таким номером уже существует в системе, возвращает ErrOrderAlreadyExists.
func (s *pgStorage) CreateOrder(ctx context.Context, number string, userID int64) error {
	const q = `INSERT INTO orders (number, user_id, status) 
	           VALUES ($1, $2, 'NEW')`

	_, err := s.db.Exec(ctx, q, number, userID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation &&
			pgErr.ConstraintName == constraintOrdersNumberKey {
			return NewErrOrderAlreadyExists(number)
		}
		return fmt.Errorf("ошибка создания заказа: %w", err)
	}

	return nil
}

// GetOrderByNumber ищет заказ по номеру и возвращает ID пользователя-владельца.
// Возвращает ErrOrderNotFound, если заказ с таким номером не найден.
func (s *pgStorage) GetOrderByNumber(ctx context.Context, number string) (int64, error) {
	const q = `SELECT user_id FROM orders WHERE number = $1`

	var userID int64
	err := s.db.QueryRow(ctx, q, number).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, NewErrOrderNotFound(number)
	}
	if err != nil {
		return 0, fmt.Errorf("ошибка запроса заказа по номеру %s: %w", number, err)
	}

	return userID, nil
}

// ListUserOrders возвращает все заказы пользователя userID,
// отсортированные от самых новых к самым старым по времени загрузки.
func (s *pgStorage) ListUserOrders(ctx context.Context, userID int64) ([]Order, error) {
	const q = `SELECT number, status, accrual, uploaded_at
	           FROM orders
	           WHERE user_id = $1
	           ORDER BY uploaded_at DESC`

	rows, err := s.db.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("ошибка запроса заказов пользователя %d: %w", userID, err)
	}
	defer rows.Close()

	orders, err := pgx.CollectRows(rows, pgx.RowToStructByName[Order])
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения ответа от хранилища: %w", err)
	}

	return orders, nil
}

// GetBalance возвращает текущий баланс и сумму всех списаний пользователя userID.
func (s *pgStorage) GetBalance(ctx context.Context, userID int64) (decimal.Decimal, decimal.Decimal, error) {
	const q = `SELECT balance, withdrawn FROM users WHERE id = $1`

	var balance, withdrawn decimal.Decimal
	err := s.db.QueryRow(ctx, q, userID).Scan(&balance, &withdrawn)
	if errors.Is(err, pgx.ErrNoRows) {
		return decimal.Zero, decimal.Zero, NewErrUserNotFound(fmt.Sprintf("id=%d", userID))
	}
	if err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("ошибка запроса баланса пользователя %d: %w", userID, err)
	}

	return balance, withdrawn, nil
}

// WithdrawPoints списывает баллы с баланса пользователя userID в счёт нового заказа orderNo.
// Выполняется в одной транзакции:
//  1. pg_advisory_xact_lock сериализует операции по одному пользователю;
//  2. условный UPDATE проверяет достаточность баланса и списывает;
//  3. INSERT в orders создаёт заказ со статусом PROCESSED
//     во избежание повторного обращения по нему в accrual-систему;
//  4. INSERT в withdrawals фиксирует факт списания.
//
// Если баланса недостаточно — возвращает ErrInsufficientFunds.
// Если заказ уже существует — возвращает ErrOrderAlreadyExists (транзакция откатывается).
func (s *pgStorage) WithdrawPoints(ctx context.Context, userID int64, orderNo string, sum decimal.Decimal) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("ошибка создания транзакции: %w", err)
	}
	defer tx.Rollback(ctx)

	// Шаг 1: блокируем пользователя по advisory lock для сериализации
	_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", userID)
	if err != nil {
		return fmt.Errorf("ошибка advisory lock: %w", err)
	}

	// Шаг 2: условный UPDATE — проверяем баланс и списываем одновременно
	// newBalance используется только для обнаружения нехватки баланса через ErrNoRows;
	// само значение далее не читается.
	var newBalance decimal.Decimal
	err = tx.QueryRow(ctx, `
		UPDATE users
		SET balance = balance - $1, withdrawn = withdrawn + $1
		WHERE id = $2 AND balance >= $1
		RETURNING balance
    `, sum, userID).Scan(&newBalance)

	if errors.Is(err, pgx.ErrNoRows) {
		return NewErrInsufficientFunds()
	}
	if err != nil {
		return fmt.Errorf("ошибка списания баллов: %w", err)
	}

	// Шаг 3: вставляем заказ (accrual не указываем — используется DEFAULT 0, аналогично CreateOrder)
	_, err = tx.Exec(ctx, `
		INSERT INTO orders (number, user_id, status)
		VALUES ($1, $2, 'PROCESSED')
    `, orderNo, userID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation &&
			pgErr.ConstraintName == constraintOrdersNumberKey {
			return NewErrOrderAlreadyExists(orderNo)
		}
		return fmt.Errorf("ошибка создания заказа при списании: %w", err)
	}

	// Шаг 4: вставляем запись о списании
	_, err = tx.Exec(ctx, `
		INSERT INTO withdrawals (user_id, order_number, sum)
		VALUES ($1, $2, $3)
    `, userID, orderNo, sum)
	if err != nil {
		return fmt.Errorf("ошибка записи о списании: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("ошибка коммита транзакции списания: %w", err)
	}

	return nil
}

// ListWithdrawals возвращает все списания пользователя userID,
// отсортированные от самых новых к самым старым по времени списания.
func (s *pgStorage) ListWithdrawals(ctx context.Context, userID int64) ([]Withdrawal, error) {
	const q = `SELECT order_number, sum, processed_at
	           FROM withdrawals
	           WHERE user_id = $1
	           ORDER BY processed_at DESC`

	rows, err := s.db.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("ошибка запроса списаний пользователя %d: %w", userID, err)
	}
	defer rows.Close()

	withdrawals, err := pgx.CollectRows(rows, pgx.RowToStructByName[Withdrawal])
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения ответа от хранилища: %w", err)
	}

	return withdrawals, nil
}

// ListPendingOrderNumbers возвращает номера всех заказов в статусах
// NEW и PROCESSING, которые требуют опроса accrual-системы.
// Используется при запуске сервиса для восстановления очереди после перезапуска.
func (s *pgStorage) ListPendingOrderNumbers(ctx context.Context) ([]string, error) {
	const q = `SELECT number FROM orders WHERE status IN ('NEW', 'PROCESSING')`

	rows, err := s.db.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("ошибка запроса незавершённых заказов: %w", err)
	}
	defer rows.Close()

	numbers, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения ответа от хранилища: %w", err)
	}

	return numbers, nil
}

// BatchUpdateOrders пакетно обновляет статусы и начисления для списка заказов.
// Для каждого заказа со статусом PROCESSED и положительным accrual
// начисляет баллы на баланс пользователя — владельца заказа.
// Вся операция выполняется в одной транзакции.
//
// Запросы выполняются без прекомпиляции, поскольку для коротких батчей (до 128 элементов)
// overhead от Prepare может превысить выгоду от повторного использования подготовленного запроса.
func (s *pgStorage) BatchUpdateOrders(ctx context.Context, updates []OrderUpdate) error {
	if len(updates) == 0 {
		return nil
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("ошибка создания транзакции: %w", err)
	}
	defer tx.Rollback(ctx)

	const qUpdateOrders = `UPDATE orders
		                   SET status = $1::order_status, accrual = $2
		                   WHERE number = $3`

	const qUpdateUsers = `UPDATE users
		                  SET balance = balance + $1
		                  WHERE id = (SELECT user_id FROM orders WHERE number = $2)`

	for _, u := range updates {
		// Обновляем статус и начисление заказа
		_, err := tx.Exec(ctx, qUpdateOrders, u.Status, u.Accrual, u.Number)
		if err != nil {
			return fmt.Errorf("ошибка обновления заказа %s: %w", u.Number, err)
		}

		// Если заказ обработан и есть начисление — добавляем баллы пользователю
		if u.Status == "PROCESSED" && u.Accrual.GreaterThan(decimal.Zero) {
			_, err = tx.Exec(ctx, qUpdateUsers, u.Accrual, u.Number)
			if err != nil {
				return fmt.Errorf("ошибка начисления баллов для заказа %s: %w", u.Number, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("ошибка коммита пакетного обновления: %w", err)
	}

	return nil
}

// Ping проверяет доступность хранилища данных.
func (s *pgStorage) Ping(ctx context.Context) error {
	return s.db.Ping(ctx)
}
