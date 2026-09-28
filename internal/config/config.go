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
// и переменных окружения. Переменные окружения имеют приоритет над флагами.
//
// Конфигурация построена по двухслойной модели:
//   - Слой чтения (envOr / os.LookupEnv) — отвечает только за факт наличия
//     переменной в окружении. Если переменная задана, её значение используется
//     как есть, даже если оно пустая строка. Если переменная не задана —
//     используется fallback (значение из флага или значение по умолчанию).
//   - Слой валидации (блок проверок в конце функции) — решает, допустимо ли
//     пустое значение для конкретного параметра. Все обязательные параметры
//     должны быть непустыми; пустое значение — ошибка конфигурации.
//
// Такое разделение позволяет в будущем менять семантику пустых значений
// на уровне бизнес-логики, не затрагивая слой чтения.
//
// Возвращает заполненную структуру ApplicationConfig или ошибку,
// если обязательные параметры не заданы или заданы пустой строкой.
func LoadApplicationConfig() (ApplicationConfig, error) {
	cfg := ApplicationConfig{}

	parseFlags(&cfg)

	// Слой чтения: env-переменные имеют приоритет над флагами.
	// envOr возвращает значение переменной, если она задана (даже пустая),
	// иначе оставляет значение из флагов.
	cfg.LogLevel = envOr("LOG_LEVEL", cfg.LogLevel)
	cfg.RunAddress = envOr("RUN_ADDRESS", cfg.RunAddress)
	cfg.DatabaseURI = envOr("DATABASE_URI", cfg.DatabaseURI)
	cfg.AccrualSystemAddress = envOr("ACCRUAL_SYSTEM_ADDRESS", cfg.AccrualSystemAddress)

	// Ключ подписи JWT: из env или значение по умолчанию.
	// Для production ключ должен быть задан через переменную окружения SIGNING_KEY.
	if v, ok := os.LookupEnv("SIGNING_KEY"); ok {
		cfg.SigningKey = []byte(v)
	} else {
		cfg.SigningKey = []byte("gophermart-signing-key")
	}

	// Значения по умолчанию, не параметризуемые извне
	cfg.CookieAuthName = "gophermart_auth"
	cfg.CookieAuthTTL = 24 * time.Hour
	cfg.ShutdownTimeout = 10 * time.Second

	// Слой валидации: ни один параметр не должен быть пустым.
	// Пустое значение может появиться двумя путями:
	//   1. Переменная окружения задана пустой строкой — envOr пропустил её,
	//      заменив значение из флага.
	//   2. Обязательный параметр не задан ни флагом, ни переменной окружения.
	// В обоих случаях — ошибка конфигурации.
	if cfg.RunAddress == "" {
		return cfg, fmt.Errorf("RUN_ADDRESS не задан или пустой (используйте флаг -a или переменную окружения RUN_ADDRESS)")
	}
	if cfg.DatabaseURI == "" {
		return cfg, fmt.Errorf("DATABASE_URI не задан или пустой (используйте флаг -d или переменную окружения DATABASE_URI)")
	}
	if cfg.AccrualSystemAddress == "" {
		return cfg, fmt.Errorf("ACCRUAL_SYSTEM_ADDRESS не задан или пустой (используйте флаг -r или переменную окружения ACCRUAL_SYSTEM_ADDRESS)")
	}
	if cfg.LogLevel == "" {
		return cfg, fmt.Errorf("LOG_LEVEL не задан или пустой (используйте флаг -l или переменную окружения LOG_LEVEL)")
	}
	if len(cfg.SigningKey) == 0 {
		return cfg, fmt.Errorf("SIGNING_KEY задан пустой строкой — укажите непустое значение")
	}

	return cfg, nil
}

// envOr возвращает значение переменной окружения, если она задана
// (даже если значение — пустая строка), иначе — fallback.
// Использует только факт наличия переменной (ok из os.LookupEnv),
// не интерпретируя содержимое: это позволяет разделить чтение конфигурации
// и валидацию/парсинг значений.
func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
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
