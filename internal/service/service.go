package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/m-j-majevsky/gophermart/internal/accrual"
	"github.com/m-j-majevsky/gophermart/internal/logger"
	"github.com/m-j-majevsky/gophermart/internal/repository"
	"github.com/shopspring/decimal"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// ServiceConfig содержит параметры работы сервисного слоя.
type ServiceConfig struct {
	Storage              repository.Storage // хранилище данных
	AccrualClient        *accrual.Client    // клиент accrual-системы
	BcryptCost           int                // стоимость bcrypt (10–14)
	AccrualQueueBuffer   int                // размер канала очереди запросов в accrual
	DBUpdateQueueBuffer  int                // размер канала очереди обновлений в БД
	DBUpdateBatchSize    int                // максимальный размер батча обновления БД
	DBUpdateFlushTimeout time.Duration      // период сброса батча в БД
	AccrualPollInterval  time.Duration      // задержка между запросами к accrual
	AccrualRetryDelay    time.Duration      // задержка перед повторным опросом нефинального заказа
}

// DefaultServiceConfig возвращает конфигурацию сервиса с значениями по умолчанию.
// Storage и AccrualClient должны быть установлены вызывающим кодом.
func DefaultServiceConfig() ServiceConfig {
	return ServiceConfig{
		BcryptCost:           12,
		AccrualQueueBuffer:   1000,
		DBUpdateQueueBuffer:  2000,
		DBUpdateBatchSize:    1000,
		DBUpdateFlushTimeout: 100 * time.Millisecond,
		AccrualPollInterval:  250 * time.Millisecond,
		AccrualRetryDelay:    3 * time.Second,
	}
}

// GopherMart — основной тип сервисного слоя.
// Содержит бизнес-логику регистрации, аутентификации, приёма заказов
// и асинхронной обработки начислений баллов.
type GopherMart struct {
	config        ServiceConfig
	accrualQueue  chan string                 // канал номеров заказов для опроса accrual
	dbUpdateQueue chan repository.OrderUpdate // канал обновлений статусов для записи в БД
}

// NewGopherMart создаёт экземпляр сервиса на основе конфигурации.
// Возвращает ошибку, если не задано хранилище.
func NewGopherMart(cfg ServiceConfig) (*GopherMart, error) {
	if cfg.Storage == nil {
		return nil, fmt.Errorf("ошибка конфигурации: не задано хранилище")
	}

	return &GopherMart{
		config:        cfg,
		accrualQueue:  make(chan string, cfg.AccrualQueueBuffer),
		dbUpdateQueue: make(chan repository.OrderUpdate, cfg.DBUpdateQueueBuffer),
	}, nil
}

// RegisterUser регистрирует нового пользователя по паре логин/пароль.
// Пароль хешируется с помощью bcrypt с указанной в конфиге стоимостью.
// Возвращает ID созданного пользователя или ошибку, если логин уже занят.
func (s *GopherMart) RegisterUser(ctx context.Context, login, password string) (int64, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.config.BcryptCost)
	if err != nil {
		return 0, fmt.Errorf("ошибка хеширования пароля: %w", err)
	}

	id, err := s.config.Storage.CreateUser(ctx, login, string(hash))
	if err != nil {
		var elt *repository.ErrLoginTaken
		if errors.As(err, &elt) {
			return 0, ErrLoginTaken
		}
		return 0, fmt.Errorf("ошибка регистрации пользователя: %w", err)
	}

	return id, nil
}

// AuthenticateUser проверяет пару логин/пароль.
// Сравнивает хеш пароля с хешем, хранящимся в БД, с помощью bcrypt.
// Возвращает ID пользователя или ErrInvalidCredentials при несовпадении.
func (s *GopherMart) AuthenticateUser(ctx context.Context, login, password string) (int64, error) {
	user, err := s.config.Storage.GetUserByLogin(ctx, login)
	if err != nil {
		var eunf *repository.ErrUserNotFound
		if errors.As(err, &eunf) {
			return 0, ErrInvalidCredentials
		}
		return 0, fmt.Errorf("ошибка поиска пользователя: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return 0, ErrInvalidCredentials
	}

	return user.ID, nil
}

// UploadOrder принимает номер заказа от пользователя userID для расчёта начисления.
// Создаёт запись в БД и ставит заказ в очередь на опрос accrual-системы.
// Возвращает nil для нового заказа, ErrOrderAlreadyExists — если заказ уже загружен
// этим пользователем, ErrOrderOwnedByAnother — если загружен другим.
func (s *GopherMart) UploadOrder(ctx context.Context, orderNumber string, userID int64) error {
	err := s.config.Storage.CreateOrder(ctx, orderNumber, userID)
	if err != nil {
		return err
	}

	s.enqueueOrder(orderNumber)
	return nil
}

// WithdrawPoints списывает баллы с баланса пользователя в счёт нового заказа.
// Проверяет баланс и создаёт заказ в одной транзакции (через хранилище).
// Заказ также ставится в очередь на опрос accrual-системы.
// Возвращает ErrInsufficientFunds при нехватке баллов,
// ErrOrderAlreadyExists — если заказ уже зарегистрирован.
func (s *GopherMart) WithdrawPoints(ctx context.Context, userID int64, orderNo string, sum decimal.Decimal) error {
	err := s.config.Storage.WithdrawPoints(ctx, userID, orderNo, sum)
	if err != nil {
		return err
	}

	s.enqueueOrder(orderNo)
	return nil
}

// ListUserOrders возвращает список заказов пользователя со статусами и начислениями,
// отсортированный от новых к старым.
func (s *GopherMart) ListUserOrders(ctx context.Context, userID int64) ([]repository.Order, error) {
	return s.config.Storage.ListUserOrders(ctx, userID)
}

// GetBalance возвращает текущий баланс и сумму всех списаний пользователя.
func (s *GopherMart) GetBalance(ctx context.Context, userID int64) (decimal.Decimal, decimal.Decimal, error) {
	return s.config.Storage.GetBalance(ctx, userID)
}

// ListWithdrawals возвращает список всех списаний пользователя,
// отсортированный от новых к старым.
func (s *GopherMart) ListWithdrawals(ctx context.Context, userID int64) ([]repository.Withdrawal, error) {
	return s.config.Storage.ListWithdrawals(ctx, userID)
}

// GetOrderByNumber возвращает ID пользователя-владельца заказа по его номеру.
// Делегирует вызов хранилищу. Используется хендлером для различения
// ситуаций «заказ загружен этим пользователем» (200) и «другим» (409).
func (s *GopherMart) GetOrderByNumber(ctx context.Context, number string) (int64, error) {
	return s.config.Storage.GetOrderByNumber(ctx, number)
}

// EnqueuePendingOrders выбирает из БД все заказы в статусах NEW и PROCESSING
// и ставит их в очередь на опрос accrual-системы. Вызывается при запуске
// сервиса для восстановления обработки после перезапуска.
func (s *GopherMart) EnqueuePendingOrders(ctx context.Context) error {
	numbers, err := s.config.Storage.ListPendingOrderNumbers(ctx)
	if err != nil {
		return fmt.Errorf("ошибка получения незавершённых заказов: %w", err)
	}

	for _, n := range numbers {
		s.enqueueOrder(n)
	}

	logger.Log.Info("pending orders enqueued for accrual processing",
		zap.Int("count", len(numbers)))

	return nil
}

// enqueueOrder ставит номер заказа в очередь на опрос accrual-системы.
// Если очередь переполнена, логирует предупреждение — заказ останется
// в БД со статусом NEW и будет обработан при рестарте.
func (s *GopherMart) enqueueOrder(orderNumber string) {
	select {
	case s.accrualQueue <- orderNumber:
		// успешно
	default:
		logger.Log.Warn("accrual queue full, order will be processed on restart",
			zap.String("order", orderNumber))
	}
}

// StartAccrualProcessor запускает две фоновые горутины:
// 1. Accrual worker — опрашивает accrual-систему для заказов из accrualQueue;
// 2. Batch processor — накапливает и пакетно записывает обновления в БД.
// Обе горутины останавливаются при отмене контекста ctx или закрытии accrualQueue.
// Вызывающий код должен дождаться завершения через WaitGroup.
func (s *GopherMart) StartAccrualProcessor(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		s.runAccrualWorker(ctx)
	}()

	go func() {
		defer wg.Done()
		s.runBatchProcessor(ctx)
	}()

	wg.Wait()
}

// StopAccrualProcessor закрывает канал accrualQueue, что приводит
// к завершению accrual worker'а и последующей остановке batch processor'а.
func (s *GopherMart) StopAccrualProcessor() {
	close(s.accrualQueue)
}

// runAccrualWorker читает номера заказов из accrualQueue и опрашивает
// accrual-систему. Для каждого заказа:
//   - при получении финального статуса (INVALID, PROCESSED) — отправляет
//     обновление в dbUpdateQueue;
//   - при нефинальном статусе (REGISTERED, PROCESSING) или коде 204 —
//     повторно ставит заказ в очередь через AccrualRetryDelay;
//   - при коде 429 — ждёт Retry-After и повторяет.
//
// Между запросами выдерживается AccrualPollInterval.
func (s *GopherMart) runAccrualWorker(ctx context.Context) {
	for {
		select {
		case orderNumber, ok := <-s.accrualQueue:
			if !ok {
				// Канал закрыт — завершаем работу
				close(s.dbUpdateQueue)
				return
			}

			s.processOneOrder(ctx, orderNumber)

			// Задержка между запросами к accrual
			select {
			case <-time.After(s.config.AccrualPollInterval):
			case <-ctx.Done():
				close(s.dbUpdateQueue)
				return
			}

		case <-ctx.Done():
			close(s.dbUpdateQueue)
			return
		}
	}
}

// processOneOrder опрашивает accrual-систему по одному заказу и маршрутизирует ответ.
func (s *GopherMart) processOneOrder(ctx context.Context, orderNumber string) {
	resp, err := s.config.AccrualClient.GetOrderAccrual(ctx, orderNumber)
	if err != nil {
		var etmr *accrual.ErrTooManyRequests
		if errors.As(err, &etmr) {
			logger.Log.Info("accrual rate limited",
				zap.String("order", orderNumber),
				zap.Int("retry_after", etmr.RetryAfter))
			// Ждём Retry-After и повторяем
			select {
			case <-time.After(time.Duration(etmr.RetryAfter) * time.Second):
				s.requeueOrder(ctx, orderNumber)
			case <-ctx.Done():
				return
			}
			return
		}

		var enr *accrual.ErrNotRegistered
		if errors.As(err, &enr) {
			logger.Log.Debug("order not registered in accrual",
				zap.String("order", orderNumber))
			s.requeueOrder(ctx, orderNumber)
			return
		}

		logger.Log.Error("accrual request failed",
			zap.String("order", orderNumber),
			zap.Error(err))
		s.requeueOrder(ctx, orderNumber)
		return
	}

	// Маппинг статусов accrual → внутренние
	var internalStatus string
	switch resp.Status {
	case "INVALID":
		internalStatus = "INVALID"
	case "PROCESSED":
		internalStatus = "PROCESSED"
	case "REGISTERED", "PROCESSING":
		internalStatus = "PROCESSING"
	default:
		internalStatus = "PROCESSING"
	}

	update := repository.OrderUpdate{
		Number:  orderNumber,
		Status:  internalStatus,
		Accrual: resp.Accrual,
	}

	// Отправляем в очередь обновления БД
	select {
	case s.dbUpdateQueue <- update:
	default:
		logger.Log.Warn("db update queue full, update dropped",
			zap.String("order", orderNumber))
	}

	// Если статус нефинальный — переотправляем в accrual-очередь
	if internalStatus == "PROCESSING" {
		s.requeueOrder(ctx, orderNumber)
	}
}

// requeueOrder повторно ставит заказ в очередь accrual после задержки AccrualRetryDelay.
// Запускает переотправку в отдельной горутине, чтобы не блокировать worker.
func (s *GopherMart) requeueOrder(ctx context.Context, orderNumber string) {
	go func() {
		select {
		case <-time.After(s.config.AccrualRetryDelay):
			s.enqueueOrder(orderNumber)
		case <-ctx.Done():
			return
		}
	}()
}

// runBatchProcessor читает обновления из dbUpdateQueue, накапливает их в батч
// и пакетно записывает в БД при достижении DBUpdateBatchSize или по таймеру
// DBUpdateFlushTimeout. Паттерн заимствован из url-shortener (fan-in).
func (s *GopherMart) runBatchProcessor(ctx context.Context) {
	batch := make([]repository.OrderUpdate, 0, s.config.DBUpdateBatchSize)
	ticker := time.NewTicker(s.config.DBUpdateFlushTimeout)
	defer ticker.Stop()

	var storageWg sync.WaitGroup

	flush := func() {
		if len(batch) == 0 {
			return
		}
		batchToStore := make([]repository.OrderUpdate, len(batch))
		copy(batchToStore, batch)
		batch = batch[:0]

		storageWg.Add(1)
		go func(b []repository.OrderUpdate) {
			defer storageWg.Done()
			if err := s.config.Storage.BatchUpdateOrders(ctx, b); err != nil {
				logger.Log.Error("batch update orders failed", zap.Error(err))
			}
		}(batchToStore)
	}

	for {
		select {
		case update, ok := <-s.dbUpdateQueue:
			if !ok {
				// Канал закрыт — финальный сброс и выход
				flush()
				storageWg.Wait()
				return
			}
			batch = append(batch, update)
			if len(batch) >= s.config.DBUpdateBatchSize {
				flush()
				ticker.Reset(s.config.DBUpdateFlushTimeout)
			}

		case <-ticker.C:
			flush()

		case <-ctx.Done():
			flush()
			storageWg.Wait()
			return
		}
	}
}

// Ping проверяет доступность хранилища данных.
func (s *GopherMart) Ping(ctx context.Context) error {
	return s.config.Storage.Ping(ctx)
}
