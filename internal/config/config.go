package config

import (
	"flag"
	"fmt"
	"os"
	"time"
)

// ApplicationConfig содержит все параметры работы сервиса.
type ApplicationConfig struct {
	LogLevel             string        // уровень логирования ("debug", "info", ...)
	RunAddress           string        // адрес и порт запуска HTTP-сервера
	DatabaseURI          string        // строка подключения к PostgreSQL
	AccrualSystemAddress string        // адрес внешней системы расчёта баллов
	SigningKey           []byte        // ключ подписи JWT-токенов
	CookieAuthName       string        // имя cookie для хранения JWT-токена аутентификации
	CookieAuthTTL        time.Duration // время жизни cookie аутентификации
	ShutdownTimeout      time.Duration // таймаут graceful shutdown
}

// LoadApplicationConfig загружает конфигурацию из переменных окружения
// и флагов командной строки и проверяет, что обязательные параметры заданы.
//
// Конфигурация разделена на два слоя:
//   - readApplicationConfig — слой чтения: считывает значения из env и флагов,
//     не интерпретируя содержимое (пустая строка считается валидным значением);
//   - validateApplicationConfig — слой валидации: проверяет, что обязательные
//     параметры непусты, и возвращает итоговую структуру или ошибку.
//
// Такое разделение позволяет менять логику валидации, не затрагивая слой чтения,
// и наоборот — например, если в будущем пустое значение станет допустимым
// для какого-то параметра, изменится только валидация.
func LoadApplicationConfig() (ApplicationConfig, error) {
	cfg := readApplicationConfig()
	return validateApplicationConfig(cfg)
}

// envOr возвращает значение переменной окружения, если она задана
// (даже если значение — пустая строка), иначе — fallback.
// Использует только факт наличия переменной (ok), не интерпретируя
// содержимое: это позволяет разделить чтение конфигурации
// и валидацию/парсинг значений.
func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

// readApplicationConfig — слой чтения: считывает значения из переменных
// окружения и флагов командной строки. Флаги командной строки имеют
// приоритет над переменными окружения: явный выбор оператора в момент
// запуска перекрывает фоновую конфигурацию среды.
//
// Логика приоритета (от низшего к высшему):
//  1. жёстко заданные значения по умолчанию;
//  2. переменные окружения — перекрывают значения по умолчанию;
//  3. флаги командной строки — перекрывают переменные окружения.
//
// Реализация: значения из env используются как значения по умолчанию
// для флагов. Если флаг задан в командной строке — flag.Parse выбирает
// его значение; если не задан — остаётся значение из env (использованное
// как default). Не валидирует значения — пустая строка считается
// валидным результатом чтения.
func readApplicationConfig() ApplicationConfig {
	cfg := ApplicationConfig{}

	// Шаг 1: жёстко заданные значения по умолчанию.
	const (
		defaultLogLevel   = "info"
		defaultRunAddress = ":8080"
	)

	// Шаг 2: переменные окружения перекрывают значения по умолчанию.
	// Результат используется как default для флагов на шаге 3.
	envLogLevel := envOr("LOG_LEVEL", defaultLogLevel)
	envRunAddress := envOr("RUN_ADDRESS", defaultRunAddress)
	envDatabaseURI := envOr("DATABASE_URI", "")
	envAccrualSystemAddress := envOr("ACCRUAL_SYSTEM_ADDRESS", "")

	// Шаг 3: флаги командной строки перекрывают переменные окружения.
	// Значения из env передаём как defaults: если флаг не задан явно,
	// остаётся значение из env; если задан — флаг побеждает.
	flag.StringVar(&cfg.RunAddress, "a", envRunAddress, "адрес и порт запуска сервиса")
	flag.StringVar(&cfg.DatabaseURI, "d", envDatabaseURI, "строка подключения к базе данных PostgreSQL")
	flag.StringVar(&cfg.AccrualSystemAddress, "r", envAccrualSystemAddress, "адрес системы расчёта начислений")
	flag.StringVar(&cfg.LogLevel, "l", envLogLevel, "уровень логирования")
	flag.Parse()

	// Ключ подписи JWT: из env или значение по умолчанию.
	// Не параметризуется флагом командной строки.
	if v, ok := os.LookupEnv("SIGNING_KEY"); ok {
		cfg.SigningKey = []byte(v)
	} else {
		cfg.SigningKey = []byte("gophermart-signing-key")
	}

	// Значения по умолчанию, не параметризуемые извне
	cfg.CookieAuthName = "gophermart_auth"
	cfg.CookieAuthTTL = 24 * time.Hour
	cfg.ShutdownTimeout = 10 * time.Second

	return cfg
}

// validateApplicationConfig — слой валидации: проверяет, что обязательные
// параметры заданы и непусты. Возвращает конфигурацию без изменений
// или ошибку с описанием недостающего параметра.
func validateApplicationConfig(cfg ApplicationConfig) (ApplicationConfig, error) {
	if cfg.RunAddress == "" {
		return cfg, fmt.Errorf("RUN_ADDRESS не задан (используйте флаг -a или переменную окружения RUN_ADDRESS)")
	}
	if cfg.DatabaseURI == "" {
		return cfg, fmt.Errorf("DATABASE_URI не задан (используйте флаг -d или переменную окружения DATABASE_URI)")
	}
	if cfg.AccrualSystemAddress == "" {
		return cfg, fmt.Errorf("ACCRUAL_SYSTEM_ADDRESS не задан (используйте флаг -r или переменную окружения ACCRUAL_SYSTEM_ADDRESS)")
	}
	if cfg.LogLevel == "" {
		return cfg, fmt.Errorf("LOG_LEVEL не задан (используйте флаг -l или переменную окружения LOG_LEVEL)")
	}
	if len(cfg.SigningKey) == 0 {
		return cfg, fmt.Errorf("SIGNING_KEY не задан (используйте переменную окружения SIGNING_KEY)")
	}

	return cfg, nil
}
