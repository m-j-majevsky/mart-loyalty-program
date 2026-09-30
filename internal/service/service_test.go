package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/m-j-majevsky/gophermart/internal/accrual"
	"github.com/m-j-majevsky/gophermart/internal/repository"
	"github.com/m-j-majevsky/gophermart/internal/service/mocks"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// ---------------------------------------------------------------------------
// Хелперы
// ---------------------------------------------------------------------------

// newTestService создаёт сервис с мок-хранилищем, мок-accrual-клиентом и no-op логгером.
func newTestService(t *testing.T) (*GopherMart, *mocks.MockStorage, *mocks.MockAccrualClient) {
	t.Helper()
	storage := mocks.NewMockStorage(t)
	accrualClient := mocks.NewMockAccrualClient(t)
	cfg := DefaultServiceConfig()
	cfg.Storage = storage
	cfg.AccrualClient = accrualClient
	cfg.Logger = zap.NewNop()
	// Ускоряем таймеры для тестов.
	cfg.AccrualPollInterval = 10 * time.Millisecond
	cfg.AccrualRetryDelay = 50 * time.Millisecond
	cfg.DBUpdateFlushTimeout = 20 * time.Millisecond
	cfg.BcryptCost = bcrypt.MinCost // быстрые хеши в тестах
	svc, err := NewGopherMart(cfg)
	require.NoError(t, err)
	return svc, storage, accrualClient
}

// ---------------------------------------------------------------------------
// NewGopherMart
// ---------------------------------------------------------------------------

func TestNewGopherMart_Success(t *testing.T) {
	// Корректная конфигурация с заполненными Storage и AccrualClient — сервис создаётся без ошибки.
	storage := mocks.NewMockStorage(t)
	accrualClient := mocks.NewMockAccrualClient(t)
	cfg := DefaultServiceConfig()
	cfg.Storage = storage
	cfg.AccrualClient = accrualClient
	cfg.Logger = zap.NewNop()
	svc, err := NewGopherMart(cfg)
	require.NoError(t, err)
	require.NotNil(t, svc)
}

func TestNewGopherMart_NilStorage(t *testing.T) {
	// nil Storage — NewGopherMart возвращает ошибку.
	accrualClient := mocks.NewMockAccrualClient(t)
	cfg := DefaultServiceConfig()
	cfg.Storage = nil
	cfg.AccrualClient = accrualClient
	_, err := NewGopherMart(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "хранилище")
}

func TestNewGopherMart_NilAccrualClient(t *testing.T) {
	// nil AccrualClient — NewGopherMart возвращает ошибку.
	storage := mocks.NewMockStorage(t)
	cfg := DefaultServiceConfig()
	cfg.Storage = storage
	cfg.AccrualClient = nil
	_, err := NewGopherMart(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accrual")
}

func TestNewGopherMart_NilLogger(t *testing.T) {
	// nil Logger — сервис использует no-op логгер, ошибки нет.
	storage := mocks.NewMockStorage(t)
	accrualClient := mocks.NewMockAccrualClient(t)
	cfg := DefaultServiceConfig()
	cfg.Storage = storage
	cfg.AccrualClient = accrualClient
	cfg.Logger = nil
	svc, err := NewGopherMart(cfg)
	require.NoError(t, err)
	assert.NotNil(t, svc.log) // no-op логгер не nil
}

// ---------------------------------------------------------------------------
// DefaultServiceConfig
// ---------------------------------------------------------------------------

func TestDefaultServiceConfig(t *testing.T) {
	// DefaultServiceConfig возвращает разумные значения по умолчанию.
	cfg := DefaultServiceConfig()
	assert.Equal(t, 12, cfg.BcryptCost)
	assert.Equal(t, 2048, cfg.AccrualQueueBuffer)
	assert.Equal(t, 5, cfg.AccrualWorkerCount)
	assert.Equal(t, 512, cfg.DBUpdateQueueBuffer)
	assert.Equal(t, 128, cfg.DBUpdateBatchSize)
	assert.Equal(t, 200*time.Millisecond, cfg.DBUpdateFlushTimeout)
	assert.Equal(t, 100*time.Millisecond, cfg.AccrualPollInterval)
	assert.Equal(t, 1*time.Second, cfg.AccrualRetryDelay)
}

// ---------------------------------------------------------------------------
// RegisterUser
// ---------------------------------------------------------------------------

func TestRegisterUser_Success(t *testing.T) {
	// Успешная регистрация: bcrypt хеширует пароль, хранилище возвращает ID.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().CreateUser(mock.Anything, "alice", mock.AnythingOfType("string")).
		Return(int64(42), nil)
	id, err := svc.RegisterUser(context.Background(), "alice", "secret")
	require.NoError(t, err)
	assert.Equal(t, int64(42), id)
}

func TestRegisterUser_LoginTaken(t *testing.T) {
	// Логин уже занят — хранилище возвращает ErrLoginTaken, сервис транслирует в ErrLoginTaken.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().CreateUser(mock.Anything, "bob", mock.AnythingOfType("string")).
		Return(int64(0), repository.NewErrLoginTaken("bob"))
	_, err := svc.RegisterUser(context.Background(), "bob", "pass")
	assert.ErrorIs(t, err, ErrLoginTaken)
}

func TestRegisterUser_DBError(t *testing.T) {
	// Произвольная ошибка БД — сервис оборачивает и возвращает.
	svc, storage, _ := newTestService(t)
	dbErr := errors.New("connection refused")
	storage.EXPECT().CreateUser(mock.Anything, "carol", mock.AnythingOfType("string")).
		Return(int64(0), dbErr)
	_, err := svc.RegisterUser(context.Background(), "carol", "pass")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrLoginTaken)
	assert.Contains(t, err.Error(), "connection refused")
}

// ---------------------------------------------------------------------------
// AuthenticateUser
// ---------------------------------------------------------------------------

func TestAuthenticateUser_Success(t *testing.T) {
	// Успешная аутентификация: хранилище возвращает пользователя, bcrypt сверяет пароль.
	svc, storage, _ := newTestService(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("mypwd"), bcrypt.MinCost)
	storage.EXPECT().GetUserByLogin(mock.Anything, "alice").
		Return(repository.User{ID: 7, Login: "alice", PasswordHash: string(hash)}, nil)
	id, err := svc.AuthenticateUser(context.Background(), "alice", "mypwd")
	require.NoError(t, err)
	assert.Equal(t, int64(7), id)
}

func TestAuthenticateUser_NotFound(t *testing.T) {
	// Пользователь не найден — сервис возвращает ErrInvalidCredentials (не раскрывает причину).
	svc, storage, _ := newTestService(t)
	storage.EXPECT().GetUserByLogin(mock.Anything, "ghost").
		Return(repository.User{}, repository.NewErrUserNotFound("ghost"))
	_, err := svc.AuthenticateUser(context.Background(), "ghost", "pwd")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestAuthenticateUser_WrongPassword(t *testing.T) {
	// Пароль не совпадает — сервис возвращает ErrInvalidCredentials.
	svc, storage, _ := newTestService(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.MinCost)
	storage.EXPECT().GetUserByLogin(mock.Anything, "alice").
		Return(repository.User{ID: 7, Login: "alice", PasswordHash: string(hash)}, nil)
	_, err := svc.AuthenticateUser(context.Background(), "alice", "wrong")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestAuthenticateUser_DBError(t *testing.T) {
	// Ошибка БД при поиске пользователя — сервис оборачивает ошибку.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().GetUserByLogin(mock.Anything, "alice").
		Return(repository.User{}, errors.New("connection lost"))
	_, err := svc.AuthenticateUser(context.Background(), "alice", "pwd")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrInvalidCredentials)
}

// ---------------------------------------------------------------------------
// UploadOrder
// ---------------------------------------------------------------------------

func TestUploadOrder_Success(t *testing.T) {
	// Успешная загрузка нового заказа — CreateOrder возвращает nil, заказ ставится в очередь.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().CreateOrder(mock.Anything, "123456789", int64(1)).
		Return(nil)
	err := svc.UploadOrder(context.Background(), "123456789", 1)
	require.NoError(t, err)
	// Заказ должен оказаться в accrualQueue.
	select {
	case n := <-svc.accrualQueue:
		assert.Equal(t, "123456789", n)
	default:
		t.Fatal("заказ не попал в очередь")
	}
}

func TestUploadOrder_Duplicate(t *testing.T) {
	// Заказ уже существует — сервис возвращает ErrOrderAlreadyExists, в очередь не ставится.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().CreateOrder(mock.Anything, "123", int64(1)).
		Return(repository.NewErrOrderAlreadyExists("123"))
	err := svc.UploadOrder(context.Background(), "123", 1)
	assert.ErrorIs(t, err, ErrOrderAlreadyExists)
	// Очередь должна быть пуста.
	select {
	case <-svc.accrualQueue:
		t.Fatal("очередь не должна содержать заказ")
	default:
	}
}

func TestUploadOrder_DBError(t *testing.T) {
	// Произвольная ошибка БД — сервис оборачивает и возвращает.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().CreateOrder(mock.Anything, "999", int64(1)).
		Return(errors.New("disk full"))
	err := svc.UploadOrder(context.Background(), "999", 1)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrOrderAlreadyExists)
}

// ---------------------------------------------------------------------------
// WithdrawPoints
// ---------------------------------------------------------------------------

func TestWithdrawPoints_Success(t *testing.T) {
	// Успешное списание — хранилище возвращает nil.
	svc, storage, _ := newTestService(t)
	sum := decimal.NewFromFloat(100.5)
	storage.EXPECT().WithdrawPoints(mock.Anything, int64(1), "ord1", sum).
		Return(nil)
	err := svc.WithdrawPoints(context.Background(), 1, "ord1", sum)
	require.NoError(t, err)
}

func TestWithdrawPoints_InsufficientFunds(t *testing.T) {
	// Недостаточно баллов — сервис транслирует ErrInsufficientFunds.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().WithdrawPoints(mock.Anything, int64(1), "ord2", mock.Anything).
		Return(repository.NewErrInsufficientFunds())
	err := svc.WithdrawPoints(context.Background(), 1, "ord2", decimal.NewFromFloat(200))
	assert.ErrorIs(t, err, ErrInsufficientFunds)
}

func TestWithdrawPoints_OrderAlreadyExists(t *testing.T) {
	// Заказ уже зарегистрирован — сервис транслирует ErrOrderAlreadyExists.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().WithdrawPoints(mock.Anything, int64(1), "ord3", mock.Anything).
		Return(repository.NewErrOrderAlreadyExists("ord3"))
	err := svc.WithdrawPoints(context.Background(), 1, "ord3", decimal.NewFromFloat(50))
	assert.ErrorIs(t, err, ErrOrderAlreadyExists)
}

func TestWithdrawPoints_DBError(t *testing.T) {
	// Произвольная ошибка БД — сервис оборачивает и возвращает.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().WithdrawPoints(mock.Anything, int64(1), "ord4", mock.Anything).
		Return(errors.New("timeout"))
	err := svc.WithdrawPoints(context.Background(), 1, "ord4", decimal.NewFromFloat(10))
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrInsufficientFunds)
	assert.NotErrorIs(t, err, ErrOrderAlreadyExists)
}

// ---------------------------------------------------------------------------
// ListUserOrders
// ---------------------------------------------------------------------------

func TestListUserOrders_WithOrders(t *testing.T) {
	// Заказы есть — сервис возвращает DTO с корректно скопированными полями.
	svc, storage, _ := newTestService(t)
	now := time.Now()
	orders := []repository.Order{
		{Number: "111", Status: "NEW", Accrual: decimal.Zero, UploadedAt: now},
		{Number: "222", Status: "PROCESSED", Accrual: decimal.NewFromFloat(500), UploadedAt: now.Add(-time.Hour)},
	}
	storage.EXPECT().ListUserOrders(mock.Anything, int64(1)).
		Return(orders, nil)
	result, err := svc.ListUserOrders(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "111", result[0].Number)
	assert.Equal(t, "NEW", result[0].Status)
	assert.Equal(t, "222", result[1].Number)
	assert.Equal(t, "PROCESSED", result[1].Status)
	assert.True(t, result[1].Accrual.Equal(decimal.NewFromFloat(500)))
}

func TestListUserOrders_Empty(t *testing.T) {
	// Заказов нет — сервис возвращает пустой слайс без ошибки.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().ListUserOrders(mock.Anything, int64(1)).
		Return([]repository.Order{}, nil)
	result, err := svc.ListUserOrders(context.Background(), 1)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestListUserOrders_DBError(t *testing.T) {
	// Ошибка БД — сервис возвращает ошибку.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().ListUserOrders(mock.Anything, int64(1)).
		Return(nil, errors.New("db error"))
	_, err := svc.ListUserOrders(context.Background(), 1)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// GetBalance
// ---------------------------------------------------------------------------

func TestGetBalance_Success(t *testing.T) {
	// Успешный запрос баланса — сервис пробрасывает значения из хранилища.
	svc, storage, _ := newTestService(t)
	bal := decimal.NewFromFloat(750)
	withdrawn := decimal.NewFromFloat(250)
	storage.EXPECT().GetBalance(mock.Anything, int64(1)).
		Return(bal, withdrawn, nil)
	b, w, err := svc.GetBalance(context.Background(), 1)
	require.NoError(t, err)
	assert.True(t, b.Equal(bal))
	assert.True(t, w.Equal(withdrawn))
}

func TestGetBalance_DBError(t *testing.T) {
	// Ошибка БД — сервис возвращает ошибку.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().GetBalance(mock.Anything, int64(1)).
		Return(decimal.Zero, decimal.Zero, errors.New("fail"))
	_, _, err := svc.GetBalance(context.Background(), 1)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// ListWithdrawals
// ---------------------------------------------------------------------------

func TestListWithdrawals_WithData(t *testing.T) {
	// Списания есть — сервис возвращает DTO с корректно скопированными полями.
	svc, storage, _ := newTestService(t)
	now := time.Now()
	ws := []repository.Withdrawal{
		{OrderNumber: "ord1", Sum: decimal.NewFromFloat(100), ProcessedAt: now},
	}
	storage.EXPECT().ListWithdrawals(mock.Anything, int64(1)).
		Return(ws, nil)
	result, err := svc.ListWithdrawals(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "ord1", result[0].OrderNumber)
	assert.True(t, result[0].Sum.Equal(decimal.NewFromFloat(100)))
}

func TestListWithdrawals_Empty(t *testing.T) {
	// Списаний нет — сервис возвращает пустой слайс.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().ListWithdrawals(mock.Anything, int64(1)).
		Return([]repository.Withdrawal{}, nil)
	result, err := svc.ListWithdrawals(context.Background(), 1)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestListWithdrawals_DBError(t *testing.T) {
	// Ошибка БД — сервис возвращает ошибку.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().ListWithdrawals(mock.Anything, int64(1)).
		Return(nil, errors.New("fail"))
	_, err := svc.ListWithdrawals(context.Background(), 1)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// GetOrderByNumber
// ---------------------------------------------------------------------------

func TestGetOrderByNumber_Found(t *testing.T) {
	// Заказ найден — сервис возвращает ID владельца.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().GetOrderByNumber(mock.Anything, "123").
		Return(int64(5), nil)
	id, err := svc.GetOrderByNumber(context.Background(), "123")
	require.NoError(t, err)
	assert.Equal(t, int64(5), id)
}

func TestGetOrderByNumber_NotFound(t *testing.T) {
	// Заказ не найден — сервис пробрасывает ErrOrderNotFound.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().GetOrderByNumber(mock.Anything, "999").
		Return(int64(0), repository.NewErrOrderNotFound("999"))
	_, err := svc.GetOrderByNumber(context.Background(), "999")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Ping
// ---------------------------------------------------------------------------

func TestPing_OK(t *testing.T) {
	// Хранилище доступно — Ping возвращает nil.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().Ping(mock.Anything).Return(nil)
	err := svc.Ping(context.Background())
	require.NoError(t, err)
}

func TestPing_Fail(t *testing.T) {
	// Хранилище недоступно — Ping возвращает ошибку.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().Ping(mock.Anything).Return(errors.New("unreachable"))
	err := svc.Ping(context.Background())
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// EnqueuePendingOrders
// ---------------------------------------------------------------------------

func TestEnqueuePendingOrders_Success(t *testing.T) {
	// Незавершённые заказы найдены — все ставятся в очередь.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().ListPendingOrderNumbers(mock.Anything).
		Return([]string{"111", "222", "333"}, nil)
	err := svc.EnqueuePendingOrders(context.Background())
	require.NoError(t, err)
	// Все три заказа должны оказаться в очереди.
	for i, expected := range []string{"111", "222", "333"} {
		select {
		case n := <-svc.accrualQueue:
			assert.Equal(t, expected, n, "заказ %d", i)
		default:
			t.Fatal("очередь пуста")
		}
	}
}

func TestEnqueuePendingOrders_DBError(t *testing.T) {
	// Ошибка БД при получении списка — сервис возвращает ошибку.
	svc, storage, _ := newTestService(t)
	storage.EXPECT().ListPendingOrderNumbers(mock.Anything).
		Return(nil, errors.New("db error"))
	err := svc.EnqueuePendingOrders(context.Background())
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// enqueueOrder
// ---------------------------------------------------------------------------

func TestEnqueueOrder_Success(t *testing.T) {
	// Очередь не переполнена, сервис работает — заказ успешно ставится в очередь.
	svc, _, _ := newTestService(t)
	svc.enqueueOrder("123")
	select {
	case n := <-svc.accrualQueue:
		assert.Equal(t, "123", n)
	default:
		t.Fatal("заказ не попал в очередь")
	}
}

func TestEnqueueOrder_Overflow(t *testing.T) {
	// Очередь переполнена — enqueueOrder не блокируется, заказ теряется (логируется warning).
	svc, _, _ := newTestService(t)
	// Заполняем очередь до отказа.
	for i := 0; i < svc.config.AccrualQueueBuffer; i++ {
		svc.accrualQueue <- "x"
	}
	// Эта отправка не должна блокировать — select default.
	svc.enqueueOrder("overflow")
	// Очередь всё ещё заполнена — "overflow" там нет.
	assert.Equal(t, svc.config.AccrualQueueBuffer, len(svc.accrualQueue))
}

func TestEnqueueOrder_Closed(t *testing.T) {
	// Сервис останавливается — enqueueOrder отменяет отправку.
	svc, _, _ := newTestService(t)
	svc.StopAccrualProcessor()
	// Не должно panic.
	svc.enqueueOrder("late")
}

// ---------------------------------------------------------------------------
// StopAccrualProcessor
// ---------------------------------------------------------------------------

func TestStopAccrualProcessor_DoubleCall(t *testing.T) {
	// Двойной вызов StopAccrualProcessor не должен panic (sync.Once).
	svc, _, _ := newTestService(t)
	svc.StopAccrualProcessor()
	svc.StopAccrualProcessor() // не panic
}

// ---------------------------------------------------------------------------
// processOneOrder
// ---------------------------------------------------------------------------

func TestProcessOneOrder_Processed(t *testing.T) {
	// Accrual возвращает PROCESSED — обновление отправляется в dbUpdateQueue, requeue нет.
	svc, _, accrualClient := newTestService(t)
	accrualClient.EXPECT().GetOrderAccrual(mock.Anything, "123").
		Return(&accrual.OrderResponse{Order: "123", Status: "PROCESSED", Accrual: decimal.NewFromFloat(500)}, nil)
	var pauseUntil atomic.Int64
	svc.processOneOrder(context.Background(), 0, "123", &pauseUntil)
	// Обновление должно попасть в dbUpdateQueue.
	select {
	case upd := <-svc.dbUpdateQueue:
		assert.Equal(t, "123", upd.Number)
		assert.Equal(t, "PROCESSED", upd.Status)
		assert.True(t, upd.Accrual.Equal(decimal.NewFromFloat(500)))
	default:
		t.Fatal("обновление не попало в dbUpdateQueue")
	}
	// accrualQueue пуста — requeue нет.
	select {
	case <-svc.accrualQueue:
		t.Fatal("не должно быть requeue для PROCESSED")
	default:
	}
}

func TestProcessOneOrder_Processing(t *testing.T) {
	// Accrual возвращает PROCESSING — обновление отправляется, заказ переотправляется в очередь.
	svc, _, accrualClient := newTestService(t)
	accrualClient.EXPECT().GetOrderAccrual(mock.Anything, "456").
		Return(&accrual.OrderResponse{Order: "456", Status: "PROCESSING", Accrual: decimal.Zero}, nil)
	var pauseUntil atomic.Int64
	svc.processOneOrder(context.Background(), 0, "456", &pauseUntil)
	// Обновление в dbUpdateQueue.
	select {
	case upd := <-svc.dbUpdateQueue:
		assert.Equal(t, "PROCESSING", upd.Status)
	default:
		t.Fatal("обновление не попало в dbUpdateQueue")
	}
	// requeue через requeueOrder (с задержкой AccrualRetryDelay).
	// Проверяем с таймаутом, т.к. requeue использует time.AfterFunc.
	select {
	case n := <-svc.accrualQueue:
		assert.Equal(t, "456", n)
	case <-time.After(2 * time.Second):
		t.Fatal("заказ не переотправлен в очередь")
	}
}

func TestProcessOneOrder_Invalid(t *testing.T) {
	// Accrual возвращает INVALID — обновление отправляется, requeue нет (финальный статус).
	svc, _, accrualClient := newTestService(t)
	accrualClient.EXPECT().GetOrderAccrual(mock.Anything, "789").
		Return(&accrual.OrderResponse{Order: "789", Status: "INVALID", Accrual: decimal.Zero}, nil)
	var pauseUntil atomic.Int64
	svc.processOneOrder(context.Background(), 0, "789", &pauseUntil)
	select {
	case upd := <-svc.dbUpdateQueue:
		assert.Equal(t, "INVALID", upd.Status)
	default:
		t.Fatal("обновление не попало в dbUpdateQueue")
	}
}

func TestProcessOneOrder_TooManyRequests(t *testing.T) {
	// Accrual возвращает 429 — pauseUntil устанавливается, заказ переотправляется с задержкой.
	svc, _, accrualClient := newTestService(t)
	accrualClient.EXPECT().GetOrderAccrual(mock.Anything, "429").
		Return(nil, accrual.NewErrTooManyRequests(5))
	var pauseUntil atomic.Int64
	svc.processOneOrder(context.Background(), 0, "429", &pauseUntil)
	// pauseUntil должно быть установлено (ненулевое).
	assert.True(t, pauseUntil.Load() > 0)
	// dbUpdateQueue пуст — обновление не отправляется при 429.
	select {
	case <-svc.dbUpdateQueue:
		t.Fatal("при 429 не должно быть обновления в БД")
	default:
	}
}

func TestProcessOneOrder_NotRegistered(t *testing.T) {
	// Accrual возвращает 204 (заказ не зарегистрирован) — заказ переотправляется с задержкой.
	svc, _, accrualClient := newTestService(t)
	accrualClient.EXPECT().GetOrderAccrual(mock.Anything, "nr1").
		Return(nil, accrual.NewErrNotRegistered())
	var pauseUntil atomic.Int64
	svc.processOneOrder(context.Background(), 0, "nr1", &pauseUntil)
	// dbUpdateQueue пуст.
	select {
	case <-svc.dbUpdateQueue:
		t.Fatal("при ErrNotRegistered не должно быть обновления в БД")
	default:
	}
	// requeue через time.AfterFunc.
	select {
	case n := <-svc.accrualQueue:
		assert.Equal(t, "nr1", n)
	case <-time.After(2 * time.Second):
		t.Fatal("заказ не переотправлен")
	}
}

func TestProcessOneOrder_UnknownStatus(t *testing.T) {
	// Accrual возвращает неизвестный статус — трактуется как PROCESSING, логируется warning.
	svc, _, accrualClient := newTestService(t)
	accrualClient.EXPECT().GetOrderAccrual(mock.Anything, "unk").
		Return(&accrual.OrderResponse{Order: "unk", Status: "WEIRD", Accrual: decimal.Zero}, nil)
	var pauseUntil atomic.Int64
	svc.processOneOrder(context.Background(), 0, "unk", &pauseUntil)
	select {
	case upd := <-svc.dbUpdateQueue:
		assert.Equal(t, "PROCESSING", upd.Status)
	default:
		t.Fatal("обновление не попало в dbUpdateQueue")
	}
}

func TestProcessOneOrder_GenericError(t *testing.T) {
	// Произвольная ошибка — заказ переотправляется с задержкой, обновление в БД не отправляется.
	svc, _, accrualClient := newTestService(t)
	accrualClient.EXPECT().GetOrderAccrual(mock.Anything, "err").
		Return(nil, errors.New("network timeout"))
	var pauseUntil atomic.Int64
	svc.processOneOrder(context.Background(), 0, "err", &pauseUntil)
	// dbUpdateQueue пуст.
	select {
	case <-svc.dbUpdateQueue:
		t.Fatal("при ошибке не должно быть обновления в БД")
	default:
	}
}

func TestProcessOneOrder_ClosedDuringDBUpdate(t *testing.T) {
	// Сервис останавливается во время отправки в dbUpdateQueue — обновление отбрасывается.
	svc, _, accrualClient := newTestService(t)
	accrualClient.EXPECT().GetOrderAccrual(mock.Anything, "closed").
		Return(&accrual.OrderResponse{Order: "closed", Status: "PROCESSED", Accrual: decimal.NewFromFloat(100)}, nil)
	// Закрываем сервис до вызова processOneOrder.
	svc.StopAccrualProcessor()
	var pauseUntil atomic.Int64
	// processOneOrder должен обработать: отправка в dbUpdateQueue не пройдёт (closed),
	// но panic не будет.
	assert.NotPanics(t, func() {
		svc.processOneOrder(context.Background(), 0, "closed", &pauseUntil)
	})
}

// ---------------------------------------------------------------------------
// pauseUntil CAS
// ---------------------------------------------------------------------------

func TestPauseUntil_CAS_MaxValue(t *testing.T) {
	// Конкурентная установка pauseUntil: 100 горутин, сохраняется максимальное значение.
	var pauseUntil atomic.Int64
	baseTime := time.Now().UnixNano()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			newPause := baseTime + int64(idx+1)*1_000_000
			for {
				old := pauseUntil.Load()
				if old >= newPause {
					break
				}
				if pauseUntil.CompareAndSwap(old, newPause) {
					break
				}
			}
		}(i)
	}
	wg.Wait()
	// Должно сохраниться максимальное значение (baseTime + 100*1_000_000).
	maxExpected := baseTime + 100*1_000_000
	assert.Equal(t, maxExpected, pauseUntil.Load())
}

// ---------------------------------------------------------------------------
// runBatchProcessor (testify/suite)
// ---------------------------------------------------------------------------

type BatchProcessorSuite struct {
	suite.Suite
	svc         *GopherMart
	storage     *mocks.MockStorage
	accrualMock *mocks.MockAccrualClient
}

func (s *BatchProcessorSuite) SetupTest() {
	s.storage = mocks.NewMockStorage(s.T())
	s.accrualMock = mocks.NewMockAccrualClient(s.T())
	cfg := DefaultServiceConfig()
	cfg.Storage = s.storage
	cfg.AccrualClient = s.accrualMock
	cfg.Logger = zap.NewNop()
	cfg.DBUpdateBatchSize = 3
	cfg.DBUpdateFlushTimeout = 50 * time.Millisecond
	svc, err := NewGopherMart(cfg)
	s.Require().NoError(err)
	s.svc = svc
}

// FlushByBatchSize: при достижении DBUpdateBatchSize батч сбрасывается в БД.
func (s *BatchProcessorSuite) TestFlushByBatchSize() {
	updates := []repository.OrderUpdate{
		{Number: "1", Status: "PROCESSED", Accrual: decimal.NewFromFloat(100)},
		{Number: "2", Status: "PROCESSED", Accrual: decimal.NewFromFloat(200)},
		{Number: "3", Status: "INVALID", Accrual: decimal.Zero},
	}
	s.storage.EXPECT().BatchUpdateOrders(mock.Anything, mock.AnythingOfType("[]repository.OrderUpdate")).
		Return(nil).Once()
	// Запускаем processor в горутине.
	go s.svc.runBatchProcessor()
	// Отправляем 3 обновления (равно DBUpdateBatchSize).
	for _, u := range updates {
		s.svc.dbUpdateQueue <- u
	}
	// Ждём, пока батч запишется.
	time.Sleep(200 * time.Millisecond)
	// Останавливаем processor.
	s.svc.StopAccrualProcessor()
	close(s.svc.dbUpdateQueue)
	time.Sleep(100 * time.Millisecond)
	s.storage.AssertExpectations(s.T())
}

// FlushByTimer: батч сбрасывается по таймеру, даже не достигнув DBUpdateBatchSize.
func (s *BatchProcessorSuite) TestFlushByTimer() {
	s.storage.EXPECT().BatchUpdateOrders(mock.Anything, mock.AnythingOfType("[]repository.OrderUpdate")).
		Return(nil).Once()
	go s.svc.runBatchProcessor()
	// Отправляем 1 обновление (меньше DBUpdateBatchSize=3).
	s.svc.dbUpdateQueue <- repository.OrderUpdate{Number: "t1", Status: "PROCESSING", Accrual: decimal.Zero}
	// Ждём срабатывания таймера (DBUpdateFlushTimeout=50ms) + запас.
	time.Sleep(200 * time.Millisecond)
	close(s.svc.dbUpdateQueue)
	time.Sleep(100 * time.Millisecond)
	s.storage.AssertExpectations(s.T())
}

// FlushOnShutdown: при закрытии dbUpdateQueue оставшийся батч сбрасывается.
func (s *BatchProcessorSuite) TestFlushOnShutdown() {
	s.storage.EXPECT().BatchUpdateOrders(mock.Anything, mock.AnythingOfType("[]repository.OrderUpdate")).
		Return(nil).Once()
	go s.svc.runBatchProcessor()
	// Отправляем 2 обновления (меньше DBUpdateBatchSize=3).
	s.svc.dbUpdateQueue <- repository.OrderUpdate{Number: "s1", Status: "PROCESSED", Accrual: decimal.NewFromFloat(50)}
	s.svc.dbUpdateQueue <- repository.OrderUpdate{Number: "s2", Status: "PROCESSING", Accrual: decimal.Zero}
	// Закрываем канал — processor должен сбросить батч и выйти.
	close(s.svc.dbUpdateQueue)
	time.Sleep(200 * time.Millisecond)
	s.storage.AssertExpectations(s.T())
}

// EmptyBatch: при закрытии пустого dbUpdateQueue flush не вызывается.
func (s *BatchProcessorSuite) TestEmptyBatch_NoFlush() {
	// Не настраиваем EXPECT для BatchUpdateOrders — метод не должен вызываться.
	go s.svc.runBatchProcessor()
	close(s.svc.dbUpdateQueue)
	time.Sleep(100 * time.Millisecond)
	s.storage.AssertExpectations(s.T())
}

// DBError: при ошибке БД processor логирует и продолжает работу.
func (s *BatchProcessorSuite) TestDBError_Continues() {
	// Первая запись — ошибка, вторая — успех.
	s.storage.EXPECT().BatchUpdateOrders(mock.Anything, mock.AnythingOfType("[]repository.OrderUpdate")).
		Return(errors.New("db error")).Once()
	s.storage.EXPECT().BatchUpdateOrders(mock.Anything, mock.AnythingOfType("[]repository.OrderUpdate")).
		Return(nil).Once()
	go s.svc.runBatchProcessor()
	// Заполняем первый батч (3 элемента = DBUpdateBatchSize).
	for i := 0; i < 3; i++ {
		s.svc.dbUpdateQueue <- repository.OrderUpdate{Number: "e1", Status: "PROCESSING"}
	}
	// Ждём первого flush.
	time.Sleep(200 * time.Millisecond)
	// Заполняем второй батч.
	for i := 0; i < 3; i++ {
		s.svc.dbUpdateQueue <- repository.OrderUpdate{Number: "e2", Status: "PROCESSING"}
	}
	time.Sleep(200 * time.Millisecond)
	close(s.svc.dbUpdateQueue)
	time.Sleep(100 * time.Millisecond)
	s.storage.AssertExpectations(s.T())
}

func TestBatchProcessorSuite(t *testing.T) {
	suite.Run(t, new(BatchProcessorSuite))
}

// ---------------------------------------------------------------------------
// accrualWorkerLoop (интеграционный сценарий через StartAccrualProcessor)
// ---------------------------------------------------------------------------

func TestAccrualWorkerLoop_ProcessAndStop(t *testing.T) {
	// Воркер обрабатывает заказ из очереди и корректно останавливается по closed.
	svc, storage, accrualMock := newTestService(t)
	storage.EXPECT().BatchUpdateOrders(mock.Anything, mock.AnythingOfType("[]repository.OrderUpdate")).
		Return(nil).Maybe()
	accrualMock.EXPECT().GetOrderAccrual(mock.Anything, "123").
		Return(&accrual.OrderResponse{Order: "123", Status: "INVALID", Accrual: decimal.Zero}, nil)
	// Ставим заказ в очередь.
	svc.enqueueOrder("123")
	// Запускаем процессор в горутине.
	ctx, cancel := context.WithCancel(context.Background())
	go svc.StartAccrualProcessor(ctx)
	// Даём время на обработку.
	time.Sleep(300 * time.Millisecond)
	// Останавливаем.
	svc.StopAccrualProcessor()
	cancel()
}

func TestAccrualWorkerLoop_PauseOn429(t *testing.T) {
	// При 429 воркер устанавливает pauseUntil и переотправляет заказ.
	svc, _, accrualMock := newTestService(t)
	accrualMock.EXPECT().GetOrderAccrual(mock.Anything, "429").
		Return(nil, accrual.NewErrTooManyRequests(1)).Maybe()
	svc.enqueueOrder("429")
	ctx, cancel := context.WithCancel(context.Background())
	go svc.StartAccrualProcessor(ctx)
	// Даём время на обработку.
	time.Sleep(300 * time.Millisecond)
	svc.StopAccrualProcessor()
	cancel()
}
