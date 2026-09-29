package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m-j-majevsky/gophermart/internal/accrual"
	"github.com/m-j-majevsky/gophermart/internal/config"
	"github.com/m-j-majevsky/gophermart/internal/handler"
	"github.com/m-j-majevsky/gophermart/internal/logger"
	"github.com/m-j-majevsky/gophermart/internal/repository"
	"github.com/m-j-majevsky/gophermart/internal/service"
	"github.com/m-j-majevsky/gophermart/migrations"

	"go.uber.org/zap"
)

// main — точка входа сервиса лояльности «Гофермарт».
// Загружает конфигурацию, инициализирует логер, накатывает миграции,
// создаёт хранилище и сервис, запускает HTTP-сервер и фоновые воркеры,
// корректно завершает работу по сигналу ОС.
func main() {
	cfg, err := config.LoadApplicationConfig()
	if err != nil {
		log.Fatal(err)
	}

	parentLogger, err := logger.Initialize(cfg.LogLevel)
	if err != nil {
		log.Fatal(err)
	}
	defer parentLogger.Sync()

	const component = "component"
	mainLogger := parentLogger.With(zap.String(component, "main"))
	handlerLogger := parentLogger.With(zap.String(component, "handler"))
	serviceLogger := parentLogger.With(zap.String(component, "service"))
	accrualLogger := parentLogger.With(zap.String(component, "accrual"))

	// Контекст с возможностью отмены по сигналу ОС
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Создаём пул соединений к PostgreSQL
	pool, err := createPool(ctx, cfg.DatabaseURI)
	if err != nil {
		mainLogger.Fatal("ошибка создания пула соединений к БД", zap.Error(err))
	}
	defer pool.Close()

	// Накатываем миграции (до создания хранилища)
	if err := migrations.RunMigrations(cfg.DatabaseURI); err != nil {
		mainLogger.Fatal("ошибка выполнения миграций", zap.Error(err))
	}

	// Создаём хранилище
	storage := repository.NewPgStorage(pool)

	// Создаём клиент accrual-системы
	accrualClient := accrual.NewClient(cfg.AccrualSystemAddress, accrualLogger)

	// Создаём сервис
	svcConfig := service.DefaultServiceConfig()
	svcConfig.Storage = storage
	svcConfig.AccrualClient = accrualClient
	svcConfig.Logger = serviceLogger

	svc, err := service.NewGopherMart(svcConfig)
	if err != nil {
		mainLogger.Fatal("ошибка инициализации сервиса", zap.Error(err))
	}

	// Восстанавливаем очередь незавершённых заказов после рестарта
	if err := svc.EnqueuePendingOrders(ctx); err != nil {
		mainLogger.Error("ошибка восстановления очереди заказов", zap.Error(err))
	}

	// Создаём роутер
	routerParams := handler.NewRouterParams(cfg, svc, handlerLogger)
	rt := handler.NewRouter(routerParams)

	// Запускаем фоновый обработчик accrual-запросов
	var backgroundWg sync.WaitGroup
	backgroundWg.Add(1)
	go func() {
		defer backgroundWg.Done()
		svc.StartAccrualProcessor(ctx)
	}()

	// Создаём HTTP-сервер
	server := &http.Server{
		Addr:    cfg.RunAddress,
		Handler: rt,
	}

	// Запускаем сервер в фоновой горутине.
	// Канал errCh сигнализирует main о фатальной ошибке запуска
	// (например, порт уже занят).
	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	mainLogger.Info("сервис запущен", zap.String("address", cfg.RunAddress))

	// Блокируем main до получения сигнала ОС или фатальной ошибки сервера
	select {
	case <-ctx.Done():
	case err := <-errCh:
		mainLogger.Fatal("ошибка HTTP-сервера", zap.Error(err))
	}

	event := zap.String("event", "shutdown")
	mainLogger.Info("получен сигнал завершения", zap.String("cause", context.Cause(ctx).Error()), event)

	// Graceful shutdown HTTP-сервера
	shutdownCtx, shutdownRelease := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownRelease()

	if err := server.Shutdown(shutdownCtx); err != nil {
		mainLogger.Error("ошибка graceful shutdown HTTP-сервера", zap.Error(err), event)
	} else {
		mainLogger.Info("HTTP-сервер остановлен корректно", event)
	}

	// Останавливаем accrual-обработчик
	svc.StopAccrualProcessor()
	mainLogger.Info("сигнал остановки accrual-обработчика отправлен", event)

	// Дожидаемся завершения фоновых горутин
	backgroundWg.Wait()

	mainLogger.Info("сервис остановлен", event)
}

// createPool создаёт настроенный пул соединений к PostgreSQL и проверяет
// подключение с помощью ping. Возвращает *pgxpool.Pool или ошибку,
// если подключение не удалось установить.
//
// Данная реализация предполагает, что конфиг соединения передается
// в DSN-строке, например, с флагом "-d" командрой строки при запуске:
// -d "postgres://gophermart:SECRET@localhost:30432/gophermart?sslmode=disable&pool_max_conns=10&pool_max_conn_lifetime=30m&pool_min_conns=2&pool_max_conn_idle_time=5m"
func createPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}

	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}

	return pool, nil
}
