package config

import (
	"flag"
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
// и переменных окружения. Переменные окружения имеют приоритет над флагами.
// Возвращает заполненную структуру ApplicationConfig или ошибку.
func LoadApplicationConfig() (ApplicationConfig, error) {
	cfg := ApplicationConfig{}

	parseFlags(&cfg)

	if v, ok := os.LookupEnv("LOG_LEVEL"); ok && v != "" {
		cfg.LogLevel = v
	}
	if v, ok := os.LookupEnv("RUN_ADDRESS"); ok && v != "" {
		cfg.RunAddress = v
	}
	if v, ok := os.LookupEnv("DATABASE_URI"); ok && v != "" {
		cfg.DatabaseURI = v
	}
	if v, ok := os.LookupEnv("ACCRUAL_SYSTEM_ADDRESS"); ok && v != "" {
		cfg.AccrualSystemAddress = v
	}

	// Ключ подписи JWT: из env или значение по умолчанию для черновой версии
	if v, ok := os.LookupEnv("SIGNING_KEY"); ok && v != "" {
		cfg.SigningKey = []byte(v)
	} else {
		cfg.SigningKey = []byte("gophermart-signing-key")
	}

	// Значения по умолчанию, не параметризуемые извне
	cfg.CookieAuthName = "gophermart_auth"
	cfg.CookieAuthTTL = 24 * time.Hour
	cfg.ShutdownTimeout = 10 * time.Second

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
