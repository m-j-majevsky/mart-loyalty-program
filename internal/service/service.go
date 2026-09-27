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

// ---------------------------------------------------------------------------
// Интерфейсы хранилища.
// Реализация — repository.pgStorage, удовлетворяющая всем интерфейсам неявно.
// ---------------------------------------------------------------------------

// UserStore — операции с пользователями: регистрация, поиск, баланс.
type UserStore interface {
	// CreateUser регистрирует нового пользователя с указанными логином и хешем пароля.
	// Возвращает ID созданного пользователя или ошибку, если логин уже занят.
	CreateUser(ctx context.Context, login, passwordHash string) (int64, error)

	// GetUserByLogin ищет пользователя по логину.
	// Возвращает структуру repository.User или ошибку, если пользователь не найден.
	GetUserByLogin(ctx context.Context, login string) (repository.User, error)

	// GetBalance возвращает текущий баланс и сумму списаний пользователя.
	GetBalance(ctx context.Context, userID int64) (balance, withdrawn decimal.Decimal, err error)
}

// OrderStore — операции с заказами: создание, поиск, листинг, пакетное обновление.
type OrderStore interface {
	// CreateOrder создаёт запись о новом заказе для пользователя userID.
	// Если заказ с таким номером уже существует, возвращает ErrOrderAlreadyExists.
	CreateOrder(ctx context.Context, number string, userID int64) error

	// GetOrderByNumber ищет заказ по номеру и возвращает ID пользователя-владельца.
	// Используется для определения, кем был загружен заказ при обработке дубликата.
	// Возвращает ErrOrderNotFound, если заказ не найден.
	GetOrderByNumber(ctx context.Context, number string) (int64, error)

	// ListUserOrders возвращает все заказы пользователя, отсортированные
	// от самых новых к самым старым по времени загрузки.
	ListUserOrders(ctx context.Context, userID int64) ([]repository.Order, error)

	// ListPendingOrderNumbers возвращает номера всех заказов в статусах
	// NEW и PROCESSING, которые требуют опроса accrual-системы.
	// Используется при запуске сервиса для восстановления очереди после перезапуска.
	ListPendingOrderNumbers(ctx context.Context) ([]string, error)

	// BatchUpdateOrders пакетно обновляет статусы и начисления для списка заказов.
	// Для каждого заказа со статусом PROCESSED начисляет баллы на баланс пользователя.
	BatchUpdateOrders(ctx context.Context, updates []repository.OrderUpdate) error
}

// WithdrawalStore — операции со списаниями: списание баллов и листинг.
type WithdrawalStore interface {
	// WithdrawPoints списывает баллы с баланса пользователя в счёт заказа orderNo.
	// Выполняется в одной транзакции: блокировка пользователя, проверка баланса,
	// списание, создание заказа и записи о списании.
	// Возвращает ErrInsufficientFunds, если баллов недостаточно,
	// или ErrOrderAlreadyExists, если заказ уже зарегистрирован.
	WithdrawPoints(ctx context.Context, userID int64, orderNo string, sum decimal.Decimal) error

	// ListWithdrawals возвращает все списания пользователя, отсортированные
	// от самых новых к самым старым по времени списания.
	ListWithdrawals(ctx context.Context, userID int64) ([]repository.Withdrawal, error)
}

// Pinger — проверка доступности хранилища.
type Pinger interface {
	// Ping проверяет доступность хранилища.
	Ping(ctx context.Context) error
}

// Storage — композиция всех интерфейсов хранилища.
// Используется в ServiceConfig для передачи хранилища в сервис.
// Реализация (repository.pgStorage) удовлетворяет этому интерфейсу неявно.
type Storage interface {
	UserStore
	OrderStore
	WithdrawalStore
	Pinger
}

// ---------------------------------------------------------------------------
// DTO сервисного слоя
// ---------------------------------------------------------------------------

// AccrualClient — интерфейс клиента внешней системы расчёта баллов.
// Реализация — accrual.Client, но интерфейс позволяет подменять клиент
// в тестах с помощью мока, не прибегая к конкретному типу.
type AccrualClient interface {
	// GetOrderAccrual запрашивает информацию о расчёте начисления для заказа.
	GetOrderAccrual(ctx context.Context, orderNumber string) (*accrual.OrderResponse, error)
}

// OrderDTO — данные заказа для передачи из сервисного слоя в хендлер.
// Используется вместо repository.Order, чтобы хендлеру не требовалось
// импортировать пакет repository.
type OrderDTO struct {
	Number     string          // номер заказа
	Status     string          // внутренний статус: NEW, PROCESSING, INVALID, PROCESSED
	Accrual    decimal.Decimal // начисленные баллы (для PROCESSED)
	UploadedAt time.Time       // время загрузки заказа в систему
}

// WithdrawalDTO — данные о списании для передачи из сервисного слоя в хендлер.
// Используется вместо repository.Withdrawal, чтобы хендлеру не требовалось
// импортировать пакет repository.
type WithdrawalDTO struct {
	OrderNumber string          // номер заказа, в счёт которого списаны баллы
	Sum         decimal.Decimal // сумма списания
	ProcessedAt time.Time       // время списания
}

// ---------------------------------------------------------------------------
// Конфигурация и основной тип сервиса
// ---------------------------------------------------------------------------

// ServiceConfig содержит параметры работы сервисного слоя.
type ServiceConfig struct {
	Storage              Storage       // хранилище данных (композиция интерфейсов)
	AccrualClient        AccrualClient // клиент accrual-системы (интерфейс, не конкретный тип)
	BcryptCost           int           // стоимость bcrypt (10–14)
	AccrualQueueBuffer   int           // размер канала очереди запросов в accrual
	DBUpdateQueueBuffer  int           // размер канала очереди обновлений в БД
	DBUpdateBatchSize    int           // максимальный размер батча обновления БД
	DBUpdateFlushTimeout time.Duration // период сброса батча в БД
	AccrualPollInterval  time.Duration // задержка между запросами к accrual
	AccrualRetryDelay    time.Duration // задержка перед повторным опросом нефинального заказа
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
//
// Поле closed — это сигнальный канал, закрытие которого означает,
// что сервис останавливается. Все горутины, отправляющие данные
// в accrualQueue, должны проверять closed перед отправкой,
// чтобы избежать panic при записи в закрытый канал.
//
// Канал accrualQueue намеренно НЕ закрывается при остановке —
// это предотвращает panic в горутинах requeueOrder (time.AfterFunc),
// которые могут сработать после StopAccrualProcessor.
// Вместо этого воркер выходит по сигналу closed и закрывает dbUpdateQueue,
// что каскадно останавливает batch processor.
type GopherMart struct {
	config        ServiceConfig
	accrualQueue  chan string                 // канал номеров заказов для опроса accrual
	dbUpdateQueue chan repository.OrderUpdate // канал обновлений статусов для записи в БД
	closed        chan struct{}               // сигнальный канал: закрыт при остановке сервиса
	closeOnce     sync.Once                   // гарантирует однократное закрытие closed
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
		closed:        make(chan struct{}),
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
// Возвращает nil для нового заказа или ErrOrderAlreadyExists — если заказ уже загружен.
// Хендлер различает «загружен этим пользователем» (200) и «другим» (409)
// с помощью GetOrderByNumber.
func (s *GopherMart) UploadOrder(ctx context.Context, orderNumber string, userID int64) error {
	err := s.config.Storage.CreateOrder(ctx, orderNumber, userID)
	if err != nil {
		// Транслируем ошибку хранилища в ошибку сервисного слоя,
		// чтобы хендлеру не приходилось импортировать пакет repository.
		var eoae *repository.ErrOrderAlreadyExists
		if errors.As(err, &eoae) {
			return ErrOrderAlreadyExists
		}
		return fmt.Errorf("ошибка создания заказа: %w", err)
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
		// Транслируем ошибки хранилища в ошибки сервисного слоя.
		var eif *repository.ErrInsufficientFunds
		if errors.As(err, &eif) {
			return ErrInsufficientFunds
		}
		var eoae *repository.ErrOrderAlreadyExists
		if errors.As(err, &eoae) {
			return ErrOrderAlreadyExists
		}
		return fmt.Errorf("ошибка списания баллов: %w", err)
	}

	s.enqueueOrder(orderNo)
	return nil
}

// ListUserOrders возвращает список заказов пользователя со статусами и начислениями,
// отсортированный от новых к старым. Возвращает DTO сервисного слоя,
// а не типы repository, чтобы хендлеру не требовалось импортировать repository.
func (s *GopherMart) ListUserOrders(ctx context.Context, userID int64) ([]OrderDTO, error) {
	orders, err := s.config.Storage.ListUserOrders(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("ошибка получения заказов пользователя: %w", err)
	}

	result := make([]OrderDTO, len(orders))
	for i, o := range orders {
		result[i] = OrderDTO{
			Number:     o.Number,
			Status:     o.Status,
			Accrual:    o.Accrual,
			UploadedAt: o.UploadedAt,
		}
	}
	return result, nil
}

// GetBalance возвращает текущий баланс и сумму всех списаний пользователя.
func (s *GopherMart) GetBalance(ctx context.Context, userID int64) (decimal.Decimal, decimal.Decimal, error) {
	return s.config.Storage.GetBalance(ctx, userID)
}

// ListWithdrawals возвращает список всех списаний пользователя,
// отсортированный от новых к старым. Возвращает DTO сервисного слоя,
// а не типы repository, чтобы хендлеру не требовалось импортировать repository.
func (s *GopherMart) ListWithdrawals(ctx context.Context, userID int64) ([]WithdrawalDTO, error) {
	withdrawals, err := s.config.Storage.ListWithdrawals(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("ошибка получения списаний пользователя: %w", err)
	}

	result := make([]WithdrawalDTO, len(withdrawals))
	for i, w := range withdrawals {
		result[i] = WithdrawalDTO{
			OrderNumber: w.OrderNumber,
			Sum:         w.Sum,
			ProcessedAt: w.ProcessedAt,
		}
	}
	return result, nil
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

	logger.Log.Info("незавершённые заказы поставлены в очередь",
		zap.Int("количество", len(numbers)))

	return nil
}

// enqueueOrder ставит номер заказа в очередь на опрос accrual-системы.
// Проверяет сигнальный канал closed: если сервис останавливается,
// отправка отменяется. Если очередь переполнена, логирует
// предупреждение — заказ останется в БД со статусом NEW
// и будет обработан при рестарте.
//
// Безопасна для вызова из любых горутин, включая time.AfterFunc,
// поскольку канал accrualQueue никогда не закрывается.
func (s *GopherMart) enqueueOrder(orderNumber string) {
	select {
	case <-s.closed:
		// сервис останавливается — не отправляем
	case s.accrualQueue <- orderNumber:
		// успешно
	default:
		logger.Log.Warn("очередь accrual переполнена, заказ будет обработан при рестарте",
			zap.String("заказ", orderNumber))
	}
}

// StartAccrualProcessor запускает две фоновые горутины:
// 1. Accrual worker — опрашивает accrual-систему для заказов из accrualQueue;
// 2. Batch processor — накапливает и пакетно записывает обновления в БД.
// Обе горутины останавливаются при отмене контекста ctx или закрытии closed.
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
		s.runBatchProcessor()
	}()

	wg.Wait()
}

// StopAccrualProcessor инициирует остановку accrual-обработчика.
// Использует sync.Once для гарантии однократного закрытия:
// закрывает сигнальный канал closed, что запрещает новые отправки
// в accrualQueue (все отправители проверяют closed перед записью).
// Accrual worker видит закрытие closed, закрывает dbUpdateQueue и завершается.
// Batch processor видит закрытие dbUpdateQueue, сбрасывает остаток батча и завершается.
//
// Канал accrualQueue намеренно НЕ закрывается — это предотвращает panic
// при записи из callback-функций time.AfterFunc в requeueOrder,
// которые могут сработать после StopAccrualProcessor.
func (s *GopherMart) StopAccrualProcessor() {
	s.closeOnce.Do(func() {
		close(s.closed)
	})
}

// runAccrualWorker читает номера заказов из accrualQueue и опрашивает
// accrual-систему. Для каждого заказа:
//   - при получении финального статуса (INVALID, PROCESSED) — отправляет
//     обновление в dbUpdateQueue;
//   - при нефинальном статусе (REGISTERED, PROCESSING) или коде 204 —
//     повторно ставит заказ в очередь через AccrualRetryDelay;
//   - при коде 429 — полная остановка на Retry-After.
//
// Между запросами выдерживается AccrualPollInterval.
// Воркер завершается при закрытии closed или отмене ctx,
// после чего закрывает dbUpdateQueue для каскадной остановки batch processor.
func (s *GopherMart) runAccrualWorker(ctx context.Context) {
	for {
		select {
		case <-s.closed:
			close(s.dbUpdateQueue)
			return
		case <-ctx.Done():
			close(s.dbUpdateQueue)
			return

		case orderNumber := <-s.accrualQueue:
			delay := s.processOneOrder(ctx, orderNumber)

			// Задержка между запросами к accrual-системе.
			// При 429 — полная остановка на Retry-After (delay > 0),
			// иначе — стандартный AccrualPollInterval.
			wait := s.config.AccrualPollInterval
			if delay > 0 {
				wait = delay
			}

			select {
			case <-time.After(wait):
			case <-s.closed:
				close(s.dbUpdateQueue)
				return
			case <-ctx.Done():
				close(s.dbUpdateQueue)
				return
			}
		}
	}
}

// processOneOrder опрашивает accrual-систему по одному заказу и маршрутизирует ответ.
// При ошибке или нефинальном статусе заказ переотправляется в очередь через
// requeueOrder с соответствующей задержкой.
//
// Возвращает задержку, на которую воркер должен приостановиться перед следующим
// запросом к accrual-системе:
//   - 0 — использовать стандартный AccrualPollInterval;
//   - >0 — при 429: воркер полностью останавливается на указанный срок (Retry-After),
//     чтобы не получить бан от accrual-системы.
func (s *GopherMart) processOneOrder(ctx context.Context, orderNumber string) time.Duration {
	resp, err := s.config.AccrualClient.GetOrderAccrual(ctx, orderNumber)
	if err != nil {
		var etmr *accrual.ErrTooManyRequests
		if errors.As(err, &etmr) {
			logger.Log.Info("превышен лимит запросов к accrual-системе, приостанавливаем воркер",
				zap.String("заказ", orderNumber),
				zap.Int("повтор_через_сек", etmr.RetryAfter))
			// Ставим в очередь с задержкой из Retry-After
			s.requeueOrder(orderNumber, time.Duration(etmr.RetryAfter)*time.Second)
			// Возвращаем задержку, чтобы воркер полностью остановился на Retry-After
			return time.Duration(etmr.RetryAfter) * time.Second
		}

		var enr *accrual.ErrNotRegistered
		if errors.As(err, &enr) {
			logger.Log.Debug("заказ не зарегистрирован в accrual-системе",
				zap.String("заказ", orderNumber))
			s.requeueOrder(orderNumber, s.config.AccrualRetryDelay)
			return 0
		}

		logger.Log.Error("ошибка запроса к accrual-системе",
			zap.String("заказ", orderNumber),
			zap.Error(err))
		s.requeueOrder(orderNumber, s.config.AccrualRetryDelay)
		return 0
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
		logger.Log.Warn("очередь обновлений БД переполнена, обновление потеряно",
			zap.String("заказ", orderNumber))
	}

	// Если статус нефинальный — переотправляем в accrual-очередь
	if internalStatus == "PROCESSING" {
		s.requeueOrder(orderNumber, s.config.AccrualRetryDelay)
	}

	return 0
}

// requeueOrder ставит заказ в очередь accrual после указанной задержки.
// Использует time.AfterFunc — таймер из стандартной библиотеки,
// который запускает callback в отдельной горутине по истечении delay.
// При срабатывании enqueueOrder проверяет сигнальный канал closed
// и безопасно отменяет отправку, если сервис останавливается.
//
// В отличие от ручной горутины с time.After, time.AfterFunc эффективнее:
// использует внутренний таймер-колесо рантайма и не требует
// отдельного select на каждый заказ.
func (s *GopherMart) requeueOrder(orderNumber string, delay time.Duration) {
	time.AfterFunc(delay, func() {
		s.enqueueOrder(orderNumber)
	})
}

// runBatchProcessor читает обновления из dbUpdateQueue, накапливает их в батч
// и пакетно записывает в БД при достижении DBUpdateBatchSize или по таймеру
// DBUpdateFlushTimeout. Паттерн заимствован из url-shortener (fan-in).
//
// Процессор завершается ТОЛЬКО при закрытии dbUpdateQueue (воркером),
// после чего сбрасывает оставшийся батч и ждёт завершения всех горутин записи.
// Использование отдельного контекста с таймаутом для flush гарантирует,
// что финальный батч будет записан даже если основной контекст уже отменён.
func (s *GopherMart) runBatchProcessor() {
	batch := make([]repository.OrderUpdate, 0, s.config.DBUpdateBatchSize)
	ticker := time.NewTicker(s.config.DBUpdateFlushTimeout)
	defer ticker.Stop()

	var storageWg sync.WaitGroup

	// flush сбрасывает накопленный батч в БД в отдельной горутине.
	// Копирует батч, чтобы не блокировать накопление следующих элементов.
	// Использует свежий контекст с таймаутом 10 секунд — это гарантирует,
	// что финальный flush при остановке сработает даже если основной
	// контекст сервиса уже отменён.
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
			flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := s.config.Storage.BatchUpdateOrders(flushCtx, b); err != nil {
				logger.Log.Error("ошибка пакетного обновления заказов", zap.Error(err))
			}
		}(batchToStore)
	}

	for {
		select {
		case update, ok := <-s.dbUpdateQueue:
			if !ok {
				// Канал закрыт воркером — финальный сброс и выход
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
		}
	}
}

// Ping проверяет доступность хранилища данных.
func (s *GopherMart) Ping(ctx context.Context) error {
	return s.config.Storage.Ping(ctx)
}
