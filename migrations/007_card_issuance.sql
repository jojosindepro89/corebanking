-- 007_card_issuance.sql: Debit & Virtual Card Issuing, PIN, Limits, and Authorization Engine Schema

CREATE TABLE IF NOT EXISTS payment_cards (
    card_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    account_id UUID NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    card_number VARCHAR(19) NOT NULL, -- Encrypted/masked 16-digit PAN (e.g., 5399-48XX-XXXX-1234)
    card_brand VARCHAR(20) NOT NULL CHECK (card_brand IN ('VISA', 'MASTERCARD', 'VERVE')),
    card_type VARCHAR(20) NOT NULL CHECK (card_type IN ('VIRTUAL', 'PHYSICAL')),
    cardholder_name VARCHAR(100) NOT NULL,
    expiry_month INT NOT NULL CHECK (expiry_month BETWEEN 1 AND 12),
    expiry_year INT NOT NULL CHECK (expiry_year >= 2026),
    cvv VARCHAR(4) NOT NULL,
    pin_hash VARCHAR(255),
    status VARCHAR(20) NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'FROZEN', 'BLOCKED', 'TERMINATED')),
    daily_limit_cents BIGINT NOT NULL DEFAULT 5000000, -- Default: N50,000 / $500
    monthly_limit_cents BIGINT NOT NULL DEFAULT 150000000,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_payment_cards_user ON payment_cards(user_id);
CREATE INDEX IF NOT EXISTS idx_payment_cards_account ON payment_cards(account_id);
CREATE INDEX IF NOT EXISTS idx_payment_cards_status ON payment_cards(status);

CREATE TABLE IF NOT EXISTS card_transactions (
    tx_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    card_id UUID NOT NULL REFERENCES payment_cards(card_id) ON DELETE CASCADE,
    account_id UUID NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    merchant_name VARCHAR(255) NOT NULL,
    merchant_category_code VARCHAR(10) NOT NULL DEFAULT '5999',
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    currency VARCHAR(3) NOT NULL DEFAULT 'NGN',
    status VARCHAR(20) NOT NULL DEFAULT 'APPROVED' CHECK (status IN ('APPROVED', 'DECLINED')),
    decline_reason VARCHAR(255),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_card_transactions_card ON card_transactions(card_id);
