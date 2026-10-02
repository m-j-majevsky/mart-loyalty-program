package config

import (
	"flag"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetFlags создаёт новый глобальный FlagSet, чтобы каждый тест
// может независимо вызывать flag.Parse() внутри readApplicationConfig.
func resetFlags() {
	flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
}

// TestEnvOr_SetEnv проверяет, что envOr возвращает значение переменной
// окружения, если она задана.
func TestEnvOr_SetEnv(t *testing.T) {
	t.Setenv("TEST_ENV_OR", "value")
	assert.Equal(t, "value", envOr("TEST_ENV_OR", "fallback"))
}

// TestEnvOr_EmptyValue проверяет, что envOr возвращает значение env,
// даже если оно пустое (использует факт наличия, а не значение).
func TestEnvOr_EmptyValue(t *testing.T) {
	t.Setenv("TEST_ENV_OR", "")
	assert.Equal(t, "", envOr("TEST_ENV_OR", "fallback"))
}

// TestEnvOr_UnsetEnv проверяет, что envOr возвращает fallback,
// если переменная окружения не задана.
func TestEnvOr_UnsetEnv(t *testing.T) {
	os.Unsetenv("TEST_ENV_OR_UNSET")
	assert.Equal(t, "fallback", envOr("TEST_ENV_OR_UNSET", "fallback"))
}

// TestValidateApplicationConfig_AllValid проверяет, что валидация
// проходит без ошибок, когда все обязательные поля заданы.
func TestValidateApplicationConfig_AllValid(t *testing.T) {
	cfg := ApplicationConfig{
		RunAddress:           ":8080",
		DatabaseURI:          "postgres://localhost/db",
		AccrualSystemAddress: "http://localhost:8081",
		LogLevel:             "info",
		SigningKey:           []byte("secret"),
	}

	result, err := validateApplicationConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, cfg, result)
}

// TestValidateApplicationConfig_MissingFields — table-driven тест,
// проверяющий, что валидация ловит каждое отсутствующее поле.
func TestValidateApplicationConfig_MissingFields(t *testing.T) {
	valid := ApplicationConfig{
		RunAddress:           ":8080",
		DatabaseURI:          "postgres://localhost/db",
		AccrualSystemAddress: "http://localhost:8081",
		LogLevel:             "info",
		SigningKey:           []byte("secret"),
	}

	tests := []struct {
		name    string
		modify  func(cfg *ApplicationConfig)
		errText string
	}{
		{"missing RUN_ADDRESS", func(c *ApplicationConfig) { c.RunAddress = "" }, "RUN_ADDRESS"},
		{"missing DATABASE_URI", func(c *ApplicationConfig) { c.DatabaseURI = "" }, "DATABASE_URI"},
		{"missing ACCRUAL", func(c *ApplicationConfig) { c.AccrualSystemAddress = "" }, "ACCRUAL_SYSTEM_ADDRESS"},
		{"missing LOG_LEVEL", func(c *ApplicationConfig) { c.LogLevel = "" }, "LOG_LEVEL"},
		{"missing SIGNING_KEY", func(c *ApplicationConfig) { c.SigningKey = nil }, "SIGNING_KEY"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.modify(&cfg)

			_, err := validateApplicationConfig(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errText)
		})
	}
}

// TestReadApplicationConfig_Defaults проверяет, что readApplicationConfig
// возвращает значения по умолчанию, когда env и флаги не заданы.
func TestReadApplicationConfig_Defaults(t *testing.T) {
	resetFlags()
	os.Args = []string{"test"}

	// Unset env-переменные, используемые в конфигурации,
	// чтобы проверить значения по умолчанию.
	// t.Setenv("...", "") не подходит — envOr использует os.LookupEnv,
	// который возвращает ok=true даже для пустой строки.
	keys := []string{"RUN_ADDRESS", "DATABASE_URI", "ACCRUAL_SYSTEM_ADDRESS", "LOG_LEVEL", "SIGNING_KEY"}
	saved := make(map[string]string)
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			saved[k] = v
		}
		os.Unsetenv(k)
	}
	defer func() {
		for k, v := range saved {
			os.Setenv(k, v)
		}
	}()

	cfg := readApplicationConfig()

	assert.Equal(t, ":8080", cfg.RunAddress)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, "gophermart-signing-key", string(cfg.SigningKey))
	assert.Equal(t, "gophermart_auth", cfg.CookieAuthName)
	assert.Equal(t, 24*time.Hour, cfg.CookieAuthTTL)
	assert.Equal(t, 10*time.Second, cfg.ShutdownTimeout)
}

// TestReadApplicationConfig_EnvOverrides проверяет, что переменные
// окружения перекрывают значения по умолчанию.
func TestReadApplicationConfig_EnvOverrides(t *testing.T) {
	resetFlags()
	os.Args = []string{"test"}

	t.Setenv("RUN_ADDRESS", ":9090")
	t.Setenv("DATABASE_URI", "postgres://user:pass@host:5432/db")
	t.Setenv("ACCRUAL_SYSTEM_ADDRESS", "http://accrual:8081")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("SIGNING_KEY", "custom-key")

	cfg := readApplicationConfig()

	assert.Equal(t, ":9090", cfg.RunAddress)
	assert.Equal(t, "postgres://user:pass@host:5432/db", cfg.DatabaseURI)
	assert.Equal(t, "http://accrual:8081", cfg.AccrualSystemAddress)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, "custom-key", string(cfg.SigningKey))
}

// TestReadApplicationConfig_FlagOverridesEnv проверяет, что флаги
// командной строки перекрывают переменные окружения.
func TestReadApplicationConfig_FlagOverridesEnv(t *testing.T) {
	resetFlags()
	os.Args = []string{"test", "-a", ":7777", "-d", "postgres://flag:5432/db", "-r", "http://flag:8081", "-l", "warn"}

	t.Setenv("RUN_ADDRESS", ":9090")
	t.Setenv("DATABASE_URI", "postgres://env:5432/db")
	t.Setenv("ACCRUAL_SYSTEM_ADDRESS", "http://env:8081")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("SIGNING_KEY", "env-key")

	cfg := readApplicationConfig()

	assert.Equal(t, ":7777", cfg.RunAddress)
	assert.Equal(t, "postgres://flag:5432/db", cfg.DatabaseURI)
	assert.Equal(t, "http://flag:8081", cfg.AccrualSystemAddress)
	assert.Equal(t, "warn", cfg.LogLevel)
	// SIGNING_KEY не параметризуется флагом — берётся из env.
	assert.Equal(t, "env-key", string(cfg.SigningKey))
}

// TestReadApplicationConfig_SigningKeyDefault проверяет, что при
// отсутствии env-переменной SIGNING_KEY используется значение по умолчанию.
func TestReadApplicationConfig_SigningKeyDefault(t *testing.T) {
	resetFlags()
	os.Args = []string{"test"}

	// Принудительно убираем SIGNING_KEY, даже если он задан в системе.
	if _, ok := os.LookupEnv("SIGNING_KEY"); ok {
		oldVal := os.Getenv("SIGNING_KEY")
		os.Unsetenv("SIGNING_KEY")
		defer os.Setenv("SIGNING_KEY", oldVal)
	}

	cfg := readApplicationConfig()
	assert.Equal(t, "gophermart-signing-key", string(cfg.SigningKey))
}

// TestLoadApplicationConfig_Success проверяет полный цикл
// загрузки конфигурации (чтение + валидация) с заданными env.
func TestLoadApplicationConfig_Success(t *testing.T) {
	resetFlags()
	os.Args = []string{"test"}

	t.Setenv("RUN_ADDRESS", ":8080")
	t.Setenv("DATABASE_URI", "postgres://localhost/db")
	t.Setenv("ACCRUAL_SYSTEM_ADDRESS", "http://localhost:8081")
	t.Setenv("LOG_LEVEL", "info")
	t.Setenv("SIGNING_KEY", "secret")

	cfg, err := LoadApplicationConfig()
	require.NoError(t, err)
	assert.Equal(t, ":8080", cfg.RunAddress)
	assert.Equal(t, "postgres://localhost/db", cfg.DatabaseURI)
	assert.Equal(t, "http://localhost:8081", cfg.AccrualSystemAddress)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, "secret", string(cfg.SigningKey))
}

// TestLoadApplicationConfig_MissingRequired проверяет, что
// LoadApplicationConfig возвращает ошибку при отсутствии DATABASE_URI.
func TestLoadApplicationConfig_MissingRequired(t *testing.T) {
	resetFlags()
	os.Args = []string{"test"}

	t.Setenv("RUN_ADDRESS", ":8080")
	// Unset DATABASE_URI to ensure it's truly absent.
	if _, ok := os.LookupEnv("DATABASE_URI"); ok {
		oldVal := os.Getenv("DATABASE_URI")
		os.Unsetenv("DATABASE_URI")
		defer os.Setenv("DATABASE_URI", oldVal)
	}
	t.Setenv("ACCRUAL_SYSTEM_ADDRESS", "http://localhost:8081")
	t.Setenv("LOG_LEVEL", "info")
	t.Setenv("SIGNING_KEY", "secret")

	_, err := LoadApplicationConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URI")
}
