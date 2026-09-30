package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// PgStorageTestSuite — тестовый набор для pgStorage.
// Каждый тест создаёт собственный экземпляр pgxmock,
// чтобы изолировать ожидания и избежать влияния между тестами.
type PgStorageTestSuite struct {
	suite.Suite
}

// TestPgStorageSuite — точка входа для запуска набора тестов.
func TestPgStorageSuite(t *testing.T) {
	suite.Run(t, new(PgStorageTestSuite))
}

// newMockStorage — вспомогательная функция, создающая свежий мок и pgStorage.
// Возвращает функцию очистки, которая закрывает мок после теста.
func (s *PgStorageTestSuite) newMockStorage() (pgxmock.PgxPoolIface, *pgStorage) {
	s.T().Helper()
	mock, err := pgxmock.NewPool()
	s.Require().NoError(err)
	s.T().Cleanup(func() { mock.Close() })
	return mock, NewPgStorage(mock)
}

// ---------------------------------------------------------------------------
// CreateUser
// ---------------------------------------------------------------------------

func (s *PgStorageTestSuite) TestCreateUser() {
	tests := []struct {
		name      string
		login     string
		password  string
		setupMock func(mock pgxmock.PgxPoolIface)
		wantID    int64
		wantErr   bool                                   // true, если ожидается ошибка
		errCheck  func(s *PgStorageTestSuite, err error) // опциональная проверка типа ошибки
	}{
		{
			name:     "успешное создание пользователя",
			login:    "alice",
			password: "$2a$10$hash",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("INSERT INTO users").
					WithArgs("alice", "$2a$10$hash").
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(42)))
			},
			wantID: 42,
		},
		{
			name:     "логин уже занят — ErrLoginTaken",
			login:    "bob",
			password: "$2a$10$hash",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("INSERT INTO users").
					WithArgs("bob", "$2a$10$hash").
					WillReturnError(&pgconn.PgError{
						Code:           pgerrcode.UniqueViolation,
						ConstraintName: constraintUsersLoginKey,
					})
			},
			wantErr: true,
			errCheck: func(s *PgStorageTestSuite, err error) {
				var target *ErrLoginTaken
				s.Require().ErrorAs(err, &target)
				s.Assert().Equal("bob", target.Login)
			},
		},
		{
			name:     "произвольная ошибка БД",
			login:    "carol",
			password: "$2a$10$hash",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("INSERT INTO users").
					WithArgs("carol", "$2a$10$hash").
					WillReturnError(errors.New("connection refused"))
			},
			wantErr: true,
			errCheck: func(s *PgStorageTestSuite, err error) {
				// Ошибка не должна быть ErrLoginTaken.
				var target *ErrLoginTaken
				s.Assert().NotErrorAs(err, &target)
			},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			mock, storage := s.newMockStorage()
			tt.setupMock(mock)

			id, err := storage.CreateUser(context.Background(), tt.login, tt.password)

			if tt.wantErr {
				s.Require().Error(err)
				if tt.errCheck != nil {
					tt.errCheck(s, err)
				}
			} else {
				s.Assert().NoError(err)
				s.Assert().Equal(tt.wantID, id)
			}

			s.Assert().NoError(mock.ExpectationsWereMet())
		})
	}
}

// ---------------------------------------------------------------------------
// GetUserByLogin
// ---------------------------------------------------------------------------

func (s *PgStorageTestSuite) TestGetUserByLogin() {
	tests := []struct {
		name      string
		login     string
		setupMock func(mock pgxmock.PgxPoolIface)
		wantUser  User
		wantErr   bool
		errCheck  func(s *PgStorageTestSuite, err error)
	}{
		{
			name:  "пользователь найден",
			login: "alice",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT id, login, password_hash, balance, withdrawn").
					WithArgs("alice").
					WillReturnRows(pgxmock.NewRows([]string{"id", "login", "password_hash", "balance", "withdrawn"}).
						AddRow(int64(1), "alice", "$2a$10$hash", decimal.NewFromFloat(100), decimal.NewFromFloat(30)))
			},
			wantUser: User{
				ID:           1,
				Login:        "alice",
				PasswordHash: "$2a$10$hash",
				Balance:      decimal.NewFromFloat(100),
				Withdrawn:    decimal.NewFromFloat(30),
			},
		},
		{
			name:  "пользователь не найден — ErrUserNotFound",
			login: "ghost",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT id, login, password_hash, balance, withdrawn").
					WithArgs("ghost").
					WillReturnRows(pgxmock.NewRows([]string{"id", "login", "password_hash", "balance", "withdrawn"}))
			},
			wantErr: true,
			errCheck: func(s *PgStorageTestSuite, err error) {
				var target *ErrUserNotFound
				s.Require().ErrorAs(err, &target)
				s.Assert().Equal("ghost", target.Login)
			},
		},
		{
			name:  "произвольная ошибка БД",
			login: "dave",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT id, login, password_hash, balance, withdrawn").
					WithArgs("dave").
					WillReturnError(errors.New("connection refused"))
			},
			wantErr: true,
			errCheck: func(s *PgStorageTestSuite, err error) {
				var target *ErrUserNotFound
				s.Assert().NotErrorAs(err, &target)
			},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			mock, storage := s.newMockStorage()
			tt.setupMock(mock)

			user, err := storage.GetUserByLogin(context.Background(), tt.login)

			if tt.wantErr {
				s.Require().Error(err)
				if tt.errCheck != nil {
					tt.errCheck(s, err)
				}
			} else {
				s.Assert().NoError(err)
				s.Assert().Equal(tt.wantUser.ID, user.ID)
				s.Assert().Equal(tt.wantUser.Login, user.Login)
				s.Assert().True(tt.wantUser.Balance.Equal(user.Balance))
				s.Assert().True(tt.wantUser.Withdrawn.Equal(user.Withdrawn))
			}

			s.Assert().NoError(mock.ExpectationsWereMet())
		})
	}
}

// ---------------------------------------------------------------------------
// CreateOrder
// ---------------------------------------------------------------------------

func (s *PgStorageTestSuite) TestCreateOrder() {
	tests := []struct {
		name      string
		number    string
		userID    int64
		setupMock func(mock pgxmock.PgxPoolIface)
		wantErr   bool
		errCheck  func(s *PgStorageTestSuite, err error)
	}{
		{
			name:   "успешное создание заказа",
			number: "12345678903",
			userID: 1,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("INSERT INTO orders").
					WithArgs("12345678903", int64(1)).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
			},
		},
		{
			name:   "заказ уже существует — ErrOrderAlreadyExists",
			number: "12345678903",
			userID: 1,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("INSERT INTO orders").
					WithArgs("12345678903", int64(1)).
					WillReturnError(&pgconn.PgError{
						Code:           pgerrcode.UniqueViolation,
						ConstraintName: constraintOrdersNumberKey,
					})
			},
			wantErr: true,
			errCheck: func(s *PgStorageTestSuite, err error) {
				var target *ErrOrderAlreadyExists
				s.Require().ErrorAs(err, &target)
				s.Assert().Equal("12345678903", target.Number)
			},
		},
		{
			name:   "произвольная ошибка БД",
			number: "999",
			userID: 1,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("INSERT INTO orders").
					WithArgs("999", int64(1)).
					WillReturnError(errors.New("connection refused"))
			},
			wantErr: true,
			errCheck: func(s *PgStorageTestSuite, err error) {
				var target *ErrOrderAlreadyExists
				s.Assert().NotErrorAs(err, &target)
			},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			mock, storage := s.newMockStorage()
			tt.setupMock(mock)

			err := storage.CreateOrder(context.Background(), tt.number, tt.userID)

			if tt.wantErr {
				s.Require().Error(err)
				if tt.errCheck != nil {
					tt.errCheck(s, err)
				}
			} else {
				s.Assert().NoError(err)
			}

			s.Assert().NoError(mock.ExpectationsWereMet())
		})
	}
}

// ---------------------------------------------------------------------------
// GetOrderByNumber
// ---------------------------------------------------------------------------

func (s *PgStorageTestSuite) TestGetOrderByNumber() {
	tests := []struct {
		name      string
		number    string
		setupMock func(mock pgxmock.PgxPoolIface)
		wantUID   int64
		wantErr   bool
		errCheck  func(s *PgStorageTestSuite, err error)
	}{
		{
			name:   "заказ найден",
			number: "12345678903",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT user_id FROM orders").
					WithArgs("12345678903").
					WillReturnRows(pgxmock.NewRows([]string{"user_id"}).AddRow(int64(7)))
			},
			wantUID: 7,
		},
		{
			name:   "заказ не найден — ErrOrderNotFound",
			number: "000",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT user_id FROM orders").
					WithArgs("000").
					WillReturnRows(pgxmock.NewRows([]string{"user_id"}))
			},
			wantErr: true,
			errCheck: func(s *PgStorageTestSuite, err error) {
				var target *ErrOrderNotFound
				s.Require().ErrorAs(err, &target)
				s.Assert().Equal("000", target.Number)
			},
		},
		{
			name:   "произвольная ошибка БД",
			number: "999",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT user_id FROM orders").
					WithArgs("999").
					WillReturnError(errors.New("connection refused"))
			},
			wantErr: true,
			errCheck: func(s *PgStorageTestSuite, err error) {
				var target *ErrOrderNotFound
				s.Assert().NotErrorAs(err, &target)
			},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			mock, storage := s.newMockStorage()
			tt.setupMock(mock)

			uid, err := storage.GetOrderByNumber(context.Background(), tt.number)

			if tt.wantErr {
				s.Require().Error(err)
				if tt.errCheck != nil {
					tt.errCheck(s, err)
				}
			} else {
				s.Assert().NoError(err)
				s.Assert().Equal(tt.wantUID, uid)
			}

			s.Assert().NoError(mock.ExpectationsWereMet())
		})
	}
}

// ---------------------------------------------------------------------------
// ListUserOrders
// ---------------------------------------------------------------------------

func (s *PgStorageTestSuite) TestListUserOrders_SuccessWithData() {
	// Возвращает список заказов пользователя, отсортированных от новых к старым.
	mock, storage := s.newMockStorage()

	now := time.Now()
	mock.ExpectQuery("SELECT number, status, accrual, uploaded_at").
		WithArgs(int64(1)).
		WillReturnRows(pgxmock.NewRows([]string{"number", "status", "accrual", "uploaded_at"}).
			AddRow("123", "PROCESSED", decimal.NewFromFloat(500), now).
			AddRow("456", "NEW", decimal.NewFromFloat(0), now.Add(-time.Hour)))

	orders, err := storage.ListUserOrders(context.Background(), 1)

	s.Require().NoError(err)
	s.Assert().Len(orders, 2)
	s.Assert().Equal("123", orders[0].Number)
	s.Assert().Equal("PROCESSED", orders[0].Status)
	s.Assert().True(decimal.NewFromFloat(500).Equal(orders[0].Accrual))
	s.Assert().Equal("456", orders[1].Number)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestListUserOrders_EmptyResult() {
	// У пользователя нет заказов — возвращается пустой слайс без ошибки.
	mock, storage := s.newMockStorage()

	mock.ExpectQuery("SELECT number, status, accrual, uploaded_at").
		WithArgs(int64(99)).
		WillReturnRows(pgxmock.NewRows([]string{"number", "status", "accrual", "uploaded_at"}))

	orders, err := storage.ListUserOrders(context.Background(), 99)

	s.Require().NoError(err)
	s.Assert().Empty(orders)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestListUserOrders_QueryError() {
	// Ошибка выполнения запроса — возвращается обёрнутая ошибка.
	mock, storage := s.newMockStorage()

	mock.ExpectQuery("SELECT number, status, accrual, uploaded_at").
		WithArgs(int64(1)).
		WillReturnError(errors.New("connection refused"))

	orders, err := storage.ListUserOrders(context.Background(), 1)

	s.Assert().Error(err)
	s.Assert().Nil(orders)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// GetBalance
// ---------------------------------------------------------------------------

func (s *PgStorageTestSuite) TestGetBalance() {
	tests := []struct {
		name          string
		userID        int64
		setupMock     func(mock pgxmock.PgxPoolIface)
		wantBalance   decimal.Decimal
		wantWithdrawn decimal.Decimal
		wantErr       bool
		errCheck      func(s *PgStorageTestSuite, err error)
	}{
		{
			name:   "баланс найден",
			userID: 1,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT balance, withdrawn").
					WithArgs(int64(1)).
					WillReturnRows(pgxmock.NewRows([]string{"balance", "withdrawn"}).
						AddRow(decimal.NewFromFloat(750), decimal.NewFromFloat(250)))
			},
			wantBalance:   decimal.NewFromFloat(750),
			wantWithdrawn: decimal.NewFromFloat(250),
		},
		{
			name:   "пользователь не найден — ErrUserNotFound",
			userID: 999,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT balance, withdrawn").
					WithArgs(int64(999)).
					WillReturnRows(pgxmock.NewRows([]string{"balance", "withdrawn"}))
			},
			wantErr: true,
			errCheck: func(s *PgStorageTestSuite, err error) {
				var target *ErrUserNotFound
				s.Require().ErrorAs(err, &target)
				s.Assert().Equal("id=999", target.Login)
			},
		},
		{
			name:   "произвольная ошибка БД",
			userID: 5,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT balance, withdrawn").
					WithArgs(int64(5)).
					WillReturnError(errors.New("timeout"))
			},
			wantErr: true,
			errCheck: func(s *PgStorageTestSuite, err error) {
				var target *ErrUserNotFound
				s.Assert().NotErrorAs(err, &target)
			},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			mock, storage := s.newMockStorage()
			tt.setupMock(mock)

			bal, wd, err := storage.GetBalance(context.Background(), tt.userID)

			if tt.wantErr {
				s.Require().Error(err)
				if tt.errCheck != nil {
					tt.errCheck(s, err)
				}
			} else {
				s.Assert().NoError(err)
				s.Assert().True(tt.wantBalance.Equal(bal))
				s.Assert().True(tt.wantWithdrawn.Equal(wd))
			}

			s.Assert().NoError(mock.ExpectationsWereMet())
		})
	}
}

// ---------------------------------------------------------------------------
// WithdrawPoints
// ---------------------------------------------------------------------------

func (s *PgStorageTestSuite) TestWithdrawPoints_Success() {
	// Полный сценарий: advisory lock → списание баланса → создание заказа →
	// запись о списании → коммит. Заказ и списание выполняются в одной транзакции.
	mock, storage := s.newMockStorage()

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs(int64(1)).
		WillReturnResult(pgxmock.NewResult("", 1))
	mock.ExpectQuery("UPDATE users").
		WithArgs(decimal.NewFromFloat(500), int64(1)).
		WillReturnRows(pgxmock.NewRows([]string{"balance"}).AddRow(decimal.NewFromFloat(250)))
	mock.ExpectExec("INSERT INTO orders").
		WithArgs("777", int64(1)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO withdrawals").
		WithArgs(int64(1), "777", decimal.NewFromFloat(500)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	err := storage.WithdrawPoints(context.Background(), 1, "777", decimal.NewFromFloat(500))

	s.Assert().NoError(err)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestWithdrawPoints_InsufficientFunds() {
	// Баланса недостаточно: условный UPDATE не находит строку (ErrNoRows),
	// возвращается ErrInsufficientFunds, транзакция откатывается.
	mock, storage := s.newMockStorage()

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs(int64(1)).
		WillReturnResult(pgxmock.NewResult("", 1))
	mock.ExpectQuery("UPDATE users").
		WithArgs(decimal.NewFromFloat(999), int64(1)).
		WillReturnRows(pgxmock.NewRows([]string{"balance"}))
	mock.ExpectRollback()

	err := storage.WithdrawPoints(context.Background(), 1, "777", decimal.NewFromFloat(999))

	s.Require().Error(err)
	var target *ErrInsufficientFunds
	s.Require().ErrorAs(err, &target)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestWithdrawPoints_DuplicateOrder() {
	// Заказ с таким номером уже существует: INSERT в orders нарушает
	// ограничение уникальности, возвращается ErrOrderAlreadyExists,
	// транзакция откатывается.
	mock, storage := s.newMockStorage()

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs(int64(1)).
		WillReturnResult(pgxmock.NewResult("", 1))
	mock.ExpectQuery("UPDATE users").
		WithArgs(decimal.NewFromFloat(50), int64(1)).
		WillReturnRows(pgxmock.NewRows([]string{"balance"}).AddRow(decimal.NewFromFloat(100)))
	mock.ExpectExec("INSERT INTO orders").
		WithArgs("777", int64(1)).
		WillReturnError(&pgconn.PgError{
			Code:           pgerrcode.UniqueViolation,
			ConstraintName: constraintOrdersNumberKey,
		})
	mock.ExpectRollback()

	err := storage.WithdrawPoints(context.Background(), 1, "777", decimal.NewFromFloat(50))

	s.Require().Error(err)
	var target *ErrOrderAlreadyExists
	s.Require().ErrorAs(err, &target)
	s.Assert().Equal("777", target.Number)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestWithdrawPoints_BeginError() {
	// Ошибка начала транзакции — функция немедленно возвращает ошибку,
	// ни одного SQL-запроса не выполняется.
	mock, storage := s.newMockStorage()

	mock.ExpectBegin().WillReturnError(errors.New("connection refused"))

	err := storage.WithdrawPoints(context.Background(), 1, "777", decimal.NewFromFloat(50))

	s.Assert().Error(err)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestWithdrawPoints_AdvisoryLockError() {
	// Ошибка advisory lock — транзакция откатывается.
	mock, storage := s.newMockStorage()

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs(int64(1)).
		WillReturnError(errors.New("lock failed"))
	mock.ExpectRollback()

	err := storage.WithdrawPoints(context.Background(), 1, "777", decimal.NewFromFloat(50))

	s.Assert().Error(err)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestWithdrawPoints_CommitError() {
	// Все шаги выполнены, но коммит завершился ошибкой —
	// функция возвращает ошибку коммита.
	mock, storage := s.newMockStorage()

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs(int64(1)).
		WillReturnResult(pgxmock.NewResult("", 1))
	mock.ExpectQuery("UPDATE users").
		WithArgs(decimal.NewFromFloat(50), int64(1)).
		WillReturnRows(pgxmock.NewRows([]string{"balance"}).AddRow(decimal.NewFromFloat(100)))
	mock.ExpectExec("INSERT INTO orders").
		WithArgs("777", int64(1)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO withdrawals").
		WithArgs(int64(1), "777", decimal.NewFromFloat(50)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))

	err := storage.WithdrawPoints(context.Background(), 1, "777", decimal.NewFromFloat(50))

	s.Assert().Error(err)
	s.Assert().Contains(err.Error(), "commit")
	s.Assert().NoError(mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// ListWithdrawals
// ---------------------------------------------------------------------------

func (s *PgStorageTestSuite) TestListWithdrawals_SuccessWithData() {
	// Возвращает список списаний, отсортированных от новых к старым.
	mock, storage := s.newMockStorage()

	now := time.Now()
	mock.ExpectQuery("SELECT order_number, sum, processed_at").
		WithArgs(int64(1)).
		WillReturnRows(pgxmock.NewRows([]string{"order_number", "sum", "processed_at"}).
			AddRow("777", decimal.NewFromFloat(500), now).
			AddRow("888", decimal.NewFromFloat(300), now.Add(-time.Hour)))

	withdrawals, err := storage.ListWithdrawals(context.Background(), 1)

	s.Require().NoError(err)
	s.Assert().Len(withdrawals, 2)
	s.Assert().Equal("777", withdrawals[0].OrderNumber)
	s.Assert().True(decimal.NewFromFloat(500).Equal(withdrawals[0].Sum))
	s.Assert().Equal("888", withdrawals[1].OrderNumber)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestListWithdrawals_EmptyResult() {
	// Списаний нет — возвращается пустой слайс без ошибки.
	mock, storage := s.newMockStorage()

	mock.ExpectQuery("SELECT order_number, sum, processed_at").
		WithArgs(int64(99)).
		WillReturnRows(pgxmock.NewRows([]string{"order_number", "sum", "processed_at"}))

	withdrawals, err := storage.ListWithdrawals(context.Background(), 99)

	s.Require().NoError(err)
	s.Assert().Empty(withdrawals)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestListWithdrawals_QueryError() {
	// Ошибка выполнения запроса — возвращается обёрнутая ошибка.
	mock, storage := s.newMockStorage()

	mock.ExpectQuery("SELECT order_number, sum, processed_at").
		WithArgs(int64(1)).
		WillReturnError(errors.New("connection refused"))

	withdrawals, err := storage.ListWithdrawals(context.Background(), 1)

	s.Assert().Error(err)
	s.Assert().Nil(withdrawals)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// ListPendingOrderNumbers
// ---------------------------------------------------------------------------

func (s *PgStorageTestSuite) TestListPendingOrderNumbers_SuccessWithData() {
	// Возвращает номера заказов в статусах NEW и PROCESSING.
	mock, storage := s.newMockStorage()

	mock.ExpectQuery("SELECT number FROM orders WHERE status IN").
		WillReturnRows(pgxmock.NewRows([]string{"number"}).
			AddRow("123").
			AddRow("456").
			AddRow("789"))

	numbers, err := storage.ListPendingOrderNumbers(context.Background())

	s.Require().NoError(err)
	s.Assert().Len(numbers, 3)
	s.Assert().Equal("123", numbers[0])
	s.Assert().Equal("456", numbers[1])
	s.Assert().Equal("789", numbers[2])
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestListPendingOrderNumbers_EmptyResult() {
	// Нет незавершённых заказов — возвращается пустой слайс без ошибки.
	mock, storage := s.newMockStorage()

	mock.ExpectQuery("SELECT number FROM orders WHERE status IN").
		WillReturnRows(pgxmock.NewRows([]string{"number"}))

	numbers, err := storage.ListPendingOrderNumbers(context.Background())

	s.Require().NoError(err)
	s.Assert().Empty(numbers)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestListPendingOrderNumbers_QueryError() {
	// Ошибка выполнения запроса — возвращается обёрнутая ошибка.
	mock, storage := s.newMockStorage()

	mock.ExpectQuery("SELECT number FROM orders WHERE status IN").
		WillReturnError(errors.New("connection refused"))

	numbers, err := storage.ListPendingOrderNumbers(context.Background())

	s.Assert().Error(err)
	s.Assert().Nil(numbers)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// BatchUpdateOrders
// ---------------------------------------------------------------------------

func (s *PgStorageTestSuite) TestBatchUpdateOrders_EmptyBatch() {
	// Пустой батч — функция немедленно возвращает nil, ни одного запроса к БД.
	mock, storage := s.newMockStorage()

	err := storage.BatchUpdateOrders(context.Background(), nil)

	s.Assert().NoError(err)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestBatchUpdateOrders_ProcessedWithAccrual() {
	// Заказ в статусе PROCESSED с положительным начислением:
	// обновляется статус заказа и баланс пользователя.
	mock, storage := s.newMockStorage()

	accrual := decimal.NewFromFloat(500)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE orders").
		WithArgs("PROCESSED", accrual, "123").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE users").
		WithArgs(accrual, "123").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	err := storage.BatchUpdateOrders(context.Background(), []OrderUpdate{
		{Number: "123", Status: "PROCESSED", Accrual: accrual},
	})

	s.Assert().NoError(err)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestBatchUpdateOrders_NonProcessedStatus() {
	// Заказ в статусе PROCESSING без начисления:
	// обновляется только статус заказа, баланс пользователя не трогается.
	mock, storage := s.newMockStorage()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE orders").
		WithArgs("PROCESSING", decimal.Zero, "456").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	err := storage.BatchUpdateOrders(context.Background(), []OrderUpdate{
		{Number: "456", Status: "PROCESSING", Accrual: decimal.Zero},
	})

	s.Assert().NoError(err)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestBatchUpdateOrders_ProcessedZeroAccrual() {
	// Заказ в статусе PROCESSED, но начисление равно нулю:
	// баланс пользователя не обновляется (дополнительный UPDATE не выполняется).
	mock, storage := s.newMockStorage()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE orders").
		WithArgs("PROCESSED", decimal.Zero, "789").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	err := storage.BatchUpdateOrders(context.Background(), []OrderUpdate{
		{Number: "789", Status: "PROCESSED", Accrual: decimal.Zero},
	})

	s.Assert().NoError(err)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestBatchUpdateOrders_MultipleUpdates() {
	// Несколько заказов в одном батче: PROCESSED с начислением и
	// PROCESSING без начисления. Для PROCESSED выполняется два UPDATE,
	// для PROCESSING — один.
	mock, storage := s.newMockStorage()

	accrual1 := decimal.NewFromFloat(500)
	mock.ExpectBegin()
	// Первый заказ: PROCESSED с начислением
	mock.ExpectExec("UPDATE orders").
		WithArgs("PROCESSED", accrual1, "123").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE users").
		WithArgs(accrual1, "123").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// Второй заказ: PROCESSING без начисления
	mock.ExpectExec("UPDATE orders").
		WithArgs("PROCESSING", decimal.Zero, "456").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// Третий заказ: PROCESSED с нулевым начислением
	mock.ExpectExec("UPDATE orders").
		WithArgs("PROCESSED", decimal.Zero, "789").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	err := storage.BatchUpdateOrders(context.Background(), []OrderUpdate{
		{Number: "123", Status: "PROCESSED", Accrual: accrual1},
		{Number: "456", Status: "PROCESSING", Accrual: decimal.Zero},
		{Number: "789", Status: "PROCESSED", Accrual: decimal.Zero},
	})

	s.Assert().NoError(err)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestBatchUpdateOrders_BeginError() {
	// Ошибка начала транзакции — функция немедленно возвращает ошибку.
	mock, storage := s.newMockStorage()

	mock.ExpectBegin().WillReturnError(errors.New("connection refused"))

	err := storage.BatchUpdateOrders(context.Background(), []OrderUpdate{
		{Number: "123", Status: "PROCESSED", Accrual: decimal.NewFromFloat(500)},
	})

	s.Assert().Error(err)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestBatchUpdateOrders_UpdateError() {
	// Ошибка обновления заказа — транзакция откатывается.
	mock, storage := s.newMockStorage()

	accrual := decimal.NewFromFloat(500)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE orders").
		WithArgs("PROCESSED", accrual, "123").
		WillReturnError(errors.New("update failed"))
	mock.ExpectRollback()

	err := storage.BatchUpdateOrders(context.Background(), []OrderUpdate{
		{Number: "123", Status: "PROCESSED", Accrual: accrual},
	})

	s.Assert().Error(err)
	s.Assert().NoError(mock.ExpectationsWereMet())
}

func (s *PgStorageTestSuite) TestBatchUpdateOrders_CommitError() {
	// Все обновления выполнены, но коммит завершился ошибкой.
	mock, storage := s.newMockStorage()

	accrual := decimal.NewFromFloat(500)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE orders").
		WithArgs("PROCESSED", accrual, "123").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE users").
		WithArgs(accrual, "123").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))

	err := storage.BatchUpdateOrders(context.Background(), []OrderUpdate{
		{Number: "123", Status: "PROCESSED", Accrual: accrual},
	})

	s.Assert().Error(err)
	s.Assert().Contains(err.Error(), "commit")
	s.Assert().NoError(mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Ping
// ---------------------------------------------------------------------------

func (s *PgStorageTestSuite) TestPing() {
	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		wantErr   bool
	}{
		{
			name: "успешный пинг",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectPing()
			},
			wantErr: false,
		},
		{
			name: "ошибка пинга",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectPing().WillReturnError(errors.New("db unavailable"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			mock, storage := s.newMockStorage()
			tt.setupMock(mock)

			err := storage.Ping(context.Background())

			if tt.wantErr {
				s.Assert().Error(err)
			} else {
				s.Assert().NoError(err)
			}
			s.Assert().NoError(mock.ExpectationsWereMet())
		})
	}
}

// ---------------------------------------------------------------------------
// Тесты ошибок хранилища (storage.go)
// ---------------------------------------------------------------------------

func TestErrLoginTaken(t *testing.T) {
	// ErrLoginTaken содержит логин, вызвавший конфликт, и корректно формирует сообщение.
	err := NewErrLoginTaken("alice")
	assert.EqualError(t, err, "логин alice уже занят")

	var target *ErrLoginTaken
	require.ErrorAs(t, err, &target)
	assert.Equal(t, "alice", target.Login)
}

func TestErrUserNotFound(t *testing.T) {
	// ErrUserNotFound содержит логин или идентификатор, не найденный в БД.
	err := NewErrUserNotFound("bob")
	assert.EqualError(t, err, "пользователь bob не найден")

	var target *ErrUserNotFound
	require.ErrorAs(t, err, &target)
	assert.Equal(t, "bob", target.Login)
}

func TestErrOrderAlreadyExists(t *testing.T) {
	// ErrOrderAlreadyExists содержит номер заказа, который уже зарегистрирован.
	err := NewErrOrderAlreadyExists("123456")
	assert.EqualError(t, err, "заказ 123456 уже зарегистрирован")

	var target *ErrOrderAlreadyExists
	require.ErrorAs(t, err, &target)
	assert.Equal(t, "123456", target.Number)
}

func TestErrOrderNotFound(t *testing.T) {
	// ErrOrderNotFound содержит номер заказа, не найденного в БД.
	err := NewErrOrderNotFound("999")
	assert.EqualError(t, err, "заказ 999 не найден")

	var target *ErrOrderNotFound
	require.ErrorAs(t, err, &target)
	assert.Equal(t, "999", target.Number)
}

func TestErrInsufficientFunds(t *testing.T) {
	// ErrInsufficientFunds — ошибка нехватки баланса, не содержит дополнительных полей.
	err := NewErrInsufficientFunds()
	assert.EqualError(t, err, "недостаточно баллов на счёте")

	var target *ErrInsufficientFunds
	require.ErrorAs(t, err, &target)
}
