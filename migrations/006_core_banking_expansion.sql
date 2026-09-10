-- 006_core_banking_expansion.sql: Enterprise Core Banking Modules Schema

-- 1. KYC_TIERS & USER_KYC_LEVELS
CREATE TABLE IF NOT EXISTS kyc_tiers (
    tier_level INT PRIMARY KEY,
    name VARCHAR(50) NOT NULL,
    daily_debit_limit_cents BIGINT NOT NULL,
    max_balance_cents BIGINT NOT NULL,
    single_tx_limit_cents BIGINT NOT NULL
);

-- Seed Standard KYC Tiers (Tier 1, Tier 2, Tier 3)
INSERT INTO kyc_tiers (tier_level, name, daily_debit_limit_cents, max_balance_cents, single_tx_limit_cents)
VALUES 
    (1, 'Tier 1 (Basic)', 5000000, 30000000, 2000000),      -- Daily: $500, Max Bal: $3,000, Single Tx: $200
    (2, 'Tier 2 (Verified)', 200000000, 500000000, 50000000),  -- Daily: $20,000, Max Bal: $50,000, Single Tx: $5,000
    (3, 'Tier 3 (Unrestricted)', 10000000000, 100000000000, 5000000000) -- Daily: $100M, Max Bal: $1B, Single Tx: $50M
ON CONFLICT (tier_level) DO NOTHING;

CREATE TABLE IF NOT EXISTS user_kyc_levels (
    user_id UUID PRIMARY KEY REFERENCES users(user_id) ON DELETE CASCADE,
    tier_level INT NOT NULL DEFAULT 1 REFERENCES kyc_tiers(tier_level),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 2. INTEREST_PRODUCTS & INTEREST_ACCRUALS
CREATE TABLE IF NOT EXISTS interest_products (
    product_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL,
    account_type VARCHAR(50) NOT NULL,
    annual_rate_percent NUMERIC(5,2) NOT NULL,
    day_count_convention VARCHAR(20) NOT NULL DEFAULT '30/360',
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed Default Interest Products
INSERT INTO interest_products (product_id, name, account_type, annual_rate_percent, day_count_convention)
VALUES 
    ('11111111-1111-1111-1111-111111111111', 'Standard Savings Account Interest', 'asset', 4.50, '30/360'),
    ('22222222-2222-2222-2222-222222222222', 'High Yield Treasury Savings', 'asset', 8.25, '30/360')
ON CONFLICT (product_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS interest_accruals (
    accrual_id BIGSERIAL PRIMARY KEY,
    account_id UUID NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    product_id UUID NOT NULL REFERENCES interest_products(product_id) ON DELETE RESTRICT,
    accrual_date DATE NOT NULL,
    balance_cents BIGINT NOT NULL,
    daily_interest_cents BIGINT NOT NULL,
    posted BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (account_id, accrual_date)
);

CREATE INDEX IF NOT EXISTS idx_interest_accruals_account ON interest_accruals(account_id);
CREATE INDEX IF NOT EXISTS idx_interest_accruals_posted ON interest_accruals(posted);

-- 3. LOANS, LOAN_SCHEDULES & REPAYMENTS
CREATE TABLE IF NOT EXISTS loans (
    loan_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    account_id UUID NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    principal_cents BIGINT NOT NULL,
    interest_rate_percent NUMERIC(5,2) NOT NULL,
    term_months INT NOT NULL,
    monthly_installment_cents BIGINT NOT NULL,
    outstanding_balance_cents BIGINT NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paid_off', 'delinquent', 'defaulted')),
    delinquency_bucket VARCHAR(50) NOT NULL DEFAULT 'current' CHECK (delinquency_bucket IN ('current', '30_days', '60_days', '90_days_default')),
    start_date TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    next_payment_due TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_loans_user ON loans(user_id);
CREATE INDEX IF NOT EXISTS idx_loans_status ON loans(status);

CREATE TABLE IF NOT EXISTS loan_schedules (
    schedule_id BIGSERIAL PRIMARY KEY,
    loan_id UUID NOT NULL REFERENCES loans(loan_id) ON DELETE CASCADE,
    installment_number INT NOT NULL,
    due_date TIMESTAMPTZ NOT NULL,
    principal_due_cents BIGINT NOT NULL,
    interest_due_cents BIGINT NOT NULL,
    total_due_cents BIGINT NOT NULL,
    paid_cents BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(20) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'paid', 'overdue')),
    UNIQUE (loan_id, installment_number)
);

CREATE INDEX IF NOT EXISTS idx_loan_schedules_loan ON loan_schedules(loan_id);

-- 4. STANDING_ORDERS
CREATE TABLE IF NOT EXISTS standing_orders (
    order_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    source_account_id UUID NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    destination_account_id UUID NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    description TEXT NOT NULL DEFAULT '',
    frequency VARCHAR(20) NOT NULL CHECK (frequency IN ('daily', 'weekly', 'monthly')),
    status VARCHAR(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'cancelled')),
    next_run_at TIMESTAMPTZ NOT NULL,
    last_run_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_standing_orders_user ON standing_orders(user_id);
CREATE INDEX IF NOT EXISTS idx_standing_orders_next_run ON standing_orders(status, next_run_at);

-- 5. BULK_PAYOUT_BATCHES & BULK_PAYOUT_ITEMS
CREATE TABLE IF NOT EXISTS bulk_payout_batches (
    batch_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    source_account_id UUID NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    title VARCHAR(255) NOT NULL,
    total_count INT NOT NULL DEFAULT 0,
    total_amount_cents BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(50) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS bulk_payout_items (
    item_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_id UUID NOT NULL REFERENCES bulk_payout_batches(batch_id) ON DELETE CASCADE,
    destination_account_id UUID NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    status VARCHAR(50) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'success', 'failed')),
    error_message TEXT,
    transaction_id UUID REFERENCES transactions(transaction_id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_bulk_payout_items_batch ON bulk_payout_items(batch_id);
