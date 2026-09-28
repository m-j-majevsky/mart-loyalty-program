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

	if err := logger.Initialize(cfg.LogLevel); err != nil {
		log.Fatal(err)
	}
	defer logger.Log.Sync()

	// Контекст с возможностью отмены по сигналу ОС
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Создаём пул соединений к PostgreSQL
	pool, err := createPool(ctx, cfg.DatabaseURI)
	if err != nil {
		logger.Log.Fatal("ошибка создания пула соединений к БД", zap.Error(err))
	}
	defer pool.Close()

	// Накатываем миграции (до создания хранилища)
	if err := migrations.RunMigrations(cfg.DatabaseURI); err != nil {
		logger.Log.Fatal("ошибка выполнения миграций", zap.Error(err))
	}

	// Создаём хранилище
	storage := repository.NewPgStorage(pool)

	// Создаём клиент accrual-системы
	accrualClient := accrual.NewClient(cfg.AccrualSystemAddress)

	// Создаём сервис
	svcConfig := service.DefaultServiceConfig()
	svcConfig.Storage = storage
	svcConfig.AccrualClient = accrualClient

	svc, err := service.NewGopherMart(svcConfig)
	if err != nil {
		logger.Log.Fatal("ошибка инициализации сервиса", zap.Error(err))
	}

	// Восстанавливаем очередь незавершённых заказов после рестарта
	if err := svc.EnqueuePendingOrders(ctx); err != nil {
		logger.Log.Error("ошибка восстановления очереди заказов", zap.Error(err))
	}

	// Создаём роутер
	routerParams := handler.NewRouterParams(cfg, svc)
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

	logger.Log.Info("сервис запущен", zap.String("address", cfg.RunAddress))

	// Блокируем main до получения сигнала ОС или фатальной ошибки сервера
	select {
	case <-ctx.Done():
	case err := <-errCh:
		logger.Log.Fatal("ошибка HTTP-сервера", zap.Error(err))
	}

	event := zap.String("event", "shutdown")
	logger.Log.Info("получен сигнал завершения", zap.String("cause", context.Cause(ctx).Error()), event)

	// Graceful shutdown HTTP-сервера
	shutdownCtx, shutdownRelease := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownRelease()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Log.Error("ошибка graceful shutdown HTTP-сервера", zap.Error(err), event)
	} else {
		logger.Log.Info("HTTP-сервер остановлен корректно", event)
	}

	// Останавливаем accrual-обработчик
	svc.StopAccrualProcessor()
	logger.Log.Info("сигнал остановки accrual-обработчика отправлен", event)

	// Дожидаемся завершения фоновых горутин
	backgroundWg.Wait()

	logger.Log.Info("сервис остановлен", event)
}

// createPool создаёт настроенный пул соединений к PostgreSQL и проверяет
// подключение с помощью ping. Возвращает *pgxpool.Pool или ошибку,
// если подключение не удалось установить.
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
