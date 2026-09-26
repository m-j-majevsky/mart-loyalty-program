// Идея реализации подсмотрена у Рафаэля Мустафина
// https://github.com/Bazys/practicum-webinars/blob/master/videos/migrate.go

package migrations

import (
	"database/sql"
	"embed"
	"fmt"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// migrations встраивает миграционные скрипты прямо в бинарник.
//
//go:embed *.sql
var migrations embed.FS

// RunMigrations накатывает все доступные миграции на базу данных,
// адрес которой передаётся в параметре dsn в виде
// "postgres://user:pass@host:5432/db?sslmode=disable".
// Если миграций нет (все уже применены), возвращает nil.
func RunMigrations(dsn string) error {
	src, err := iofs.New(migrations, ".")
	if err != nil {
		return fmt.Errorf("ошибка доступа к источнику данных миграции: %w", err)
	}

	// Очищаем DSN от лишних параметров
	if dsn, err = filterDSNToSSLMode(dsn); err != nil {
		return fmt.Errorf("ошибка обработки dsn: %w", err)
	}

	// migrate работает через database/sql, а не через нативный pgxpool:
	// драйвер pgx5 (pgx/v5/stdlib) оборачивает pgx в интерфейс sql.DB.
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("ошибка доступа к pgx через интерфейс sql.DB: %w", err)
	}
	defer db.Close()

	// pgxmigrate.WithInstance превращает *sql.DB в database.Driver для migrate.
	dbDriver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		return fmt.Errorf("ошибка получения драйвера для миграции: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "pgx5", dbDriver)
	if err != nil {
		return fmt.Errorf("ошибка создания инстанса миграции: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("ошибка migrate up: %w", err)
	}
	return nil
}

// filterDSNToSSLMode возвращает DSN, в котором из query-параметров
// остаётся только sslmode (если он есть), а остальные удаляются.
// Нужна для корректной работы pgxmigrate.WithInstance,
// которая не понимает, например, pool_max_conn_idle_time.
func filterDSNToSSLMode(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}

	q := u.Query()

	sslmodeVal := ""
	if vals, ok := q["sslmode"]; ok && len(vals) > 0 {
		sslmodeVal = vals[0]
	}

	q = make(url.Values)
	if sslmodeVal != "" {
		q.Set("sslmode", sslmodeVal)
	}

	u.RawQuery = q.Encode()
	return u.String(), nil
}
