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

// LoadApplicationConfig загружает конфигурацию из флагов командной строки
// и переменных окружения и проверяет, что обязательные параметры заданы.
//
// Конфигурация разделена на два слоя:
//   - readApplicationConfig — слой чтения: считывает значения из флагов и env,
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

// readApplicationConfig — слой чтения: считывает значения из флагов
// командной строки и переменных окружения. Переменные окружения имеют
// приоритет над флагами. Не валидирует значения — пустая строка
// считается валидным результатом чтения.
func readApplicationConfig() ApplicationConfig {
	cfg := ApplicationConfig{}

	// Флаги командной строки — значения по умолчанию
	parseFlags(&cfg)

	// Переменные окружения имеют приоритет над флагами.
	// envOr берёт значение из окружения, если переменная задана (даже пустая),
	// иначе оставляет значение из флагов.
	cfg.LogLevel = envOr("LOG_LEVEL", cfg.LogLevel)
	cfg.RunAddress = envOr("RUN_ADDRESS", cfg.RunAddress)
	cfg.DatabaseURI = envOr("DATABASE_URI", cfg.DatabaseURI)
	cfg.AccrualSystemAddress = envOr("ACCRUAL_SYSTEM_ADDRESS", cfg.AccrualSystemAddress)

	// Ключ подписи JWT: из env или значение по умолчанию.
	// Используем os.LookupEnv напрямую, чтобы различать «не задана»
	// (использовать дефолт) и «задана пустой» (валидация поймает).
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

// parseFlags регистрирует и разбирает флаги командной строки,
// записывая результат в структуру cfg.
func parseFlags(cfg *ApplicationConfig) {
	flag.StringVar(&cfg.RunAddress, "a", ":8080", "адрес и порт запуска сервиса")
	flag.StringVar(&cfg.DatabaseURI, "d", "", "строка подключения к базе данных PostgreSQL")
	flag.StringVar(&cfg.AccrualSystemAddress, "r", "", "адрес системы расчёта начислений")
	flag.StringVar(&cfg.LogLevel, "l", "info", "уровень логирования")
	flag.Parse()
}
