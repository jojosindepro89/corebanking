-- 009_fx_engine.sql: Schema for Multi-Currency FX Rates, Quotes, and Swap Execution

CREATE TABLE IF NOT EXISTS fx_rates (
    rate_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    from_currency VARCHAR(10) NOT NULL,
    to_currency VARCHAR(10) NOT NULL,
    rate NUMERIC(18, 8) NOT NULL,
    fee_percent NUMERIC(5, 2) NOT NULL DEFAULT 0.50,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT unique_fx_pair UNIQUE(from_currency, to_currency)
);

CREATE TABLE IF NOT EXISTS fx_quotes (
    quote_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    from_account_id UUID NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    to_account_id UUID NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    from_currency VARCHAR(10) NOT NULL,
    to_currency VARCHAR(10) NOT NULL,
    from_amount_cents BIGINT NOT NULL,
    to_amount_cents BIGINT NOT NULL,
    fee_cents BIGINT NOT NULL DEFAULT 0,
    exchange_rate NUMERIC(18, 8) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'executed', 'expired')),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_fx_quotes_user_id ON fx_quotes(user_id);
CREATE INDEX IF NOT EXISTS idx_fx_quotes_status ON fx_quotes(status);
CREATE INDEX IF NOT EXISTS idx_fx_quotes_expires_at ON fx_quotes(expires_at);

-- Seed default FX Rates
INSERT INTO fx_rates (from_currency, to_currency, rate, fee_percent)
VALUES 
    ('USD', 'NGN', 1550.00, 0.50),
    ('NGN', 'USD', 0.00064516, 0.50),
    ('USD', 'EUR', 0.91500000, 0.50),
    ('EUR', 'USD', 1.09300000, 0.50),
    ('USD', 'GBP', 0.78500000, 0.50),
    ('GBP', 'USD', 1.27400000, 0.50),
    ('USD', 'USDC', 1.00000000, 0.10),
    ('USDC', 'USD', 1.00000000, 0.10),
    ('BTC', 'USD', 64200.00, 0.80),
    ('USD', 'BTC', 0.000015576, 0.80)
ON CONFLICT (from_currency, to_currency) DO UPDATE 
SET rate = EXCLUDED.rate, fee_percent = EXCLUDED.fee_percent, updated_at = NOW();
