-- Начальная схема базы данных сервиса лояльности «Гофермарт»

-- Таблица пользователей.
-- login уникален; password_hash хранит bcrypt-хеш пароля.
-- balance и withdrawn ведут текущий баланс и суммарное списание баллов.
CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL     PRIMARY KEY,
    login         TEXT          NOT NULL UNIQUE,
    password_hash TEXT          NOT NULL,
    balance       NUMERIC(12,2) NOT NULL DEFAULT 0,
    withdrawn     NUMERIC(12,2) NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ   NOT NULL DEFAULT now()
);

-- Таблица заказов.
-- number — номер заказа (уникален глобально).
-- status хранит внутренний статус: NEW, PROCESSING, INVALID, PROCESSED.
-- accrual — начисленные баллы (заполняется при переходе в PROCESSED).
CREATE TABLE IF NOT EXISTS orders (
    id          BIGSERIAL     PRIMARY KEY,
    number      TEXT          NOT NULL UNIQUE,
    user_id     BIGINT        NOT NULL REFERENCES users(id),
    status      TEXT          NOT NULL DEFAULT 'NEW',
    accrual     NUMERIC(12,2) NOT NULL DEFAULT 0,
    uploaded_at TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE INDEX idx_orders_user_id ON orders(user_id);
CREATE INDEX idx_orders_status  ON orders(status);

-- Таблица списаний баллов.
-- Каждая запись — факт списания баллов в счёт заказа.
CREATE TABLE IF NOT EXISTS withdrawals (
    id           BIGSERIAL     PRIMARY KEY,
    user_id      BIGINT        NOT NULL REFERENCES users(id),
    order_number TEXT          NOT NULL,
    sum          NUMERIC(12,2) NOT NULL,
    processed_at TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE INDEX idx_withdrawals_user_id ON withdrawals(user_id);
