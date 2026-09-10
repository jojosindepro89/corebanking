-- 004_nip_integration.sql: Schema for NIBSS Instant Payment (NIP) Interbank Transfer Integration

-- 1. NIP_TRANSACTIONS
CREATE TABLE IF NOT EXISTS nip_transactions (
    nip_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id VARCHAR(128) NOT NULL UNIQUE,
    transaction_ref VARCHAR(128) NOT NULL UNIQUE,
    user_id UUID NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    source_account_id UUID NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    destination_bank_code VARCHAR(10) NOT NULL,
    destination_account_number VARCHAR(20) NOT NULL,
    destination_account_name VARCHAR(255) NOT NULL,
    amount_cents BIGINT NOT NULL,
    direction VARCHAR(20) NOT NULL CHECK (direction IN ('outbound', 'inbound')),
    status VARCHAR(50) NOT NULL CHECK (status IN ('pending_debit', 'pending_nibss', 'completed', 'failed', 'pending_reconciliation')),
    ledger_tx_id UUID REFERENCES transactions(transaction_id) ON DELETE SET NULL,
    reversal_tx_id UUID REFERENCES transactions(transaction_id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_nip_tx_session ON nip_transactions(session_id);
CREATE INDEX IF NOT EXISTS idx_nip_tx_ref ON nip_transactions(transaction_ref);
CREATE INDEX IF NOT EXISTS idx_nip_tx_status ON nip_transactions(status);
CREATE INDEX IF NOT EXISTS idx_nip_tx_user ON nip_transactions(user_id);

-- 2. NIP_NAME_ENQUIRIES
CREATE TABLE IF NOT EXISTS nip_name_enquiries (
    enquiry_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id VARCHAR(128) NOT NULL UNIQUE,
    user_id UUID NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    bank_code VARCHAR(10) NOT NULL,
    account_number VARCHAR(20) NOT NULL,
    account_name VARCHAR(255) NOT NULL,
    response_code VARCHAR(10) NOT NULL DEFAULT '00',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_nip_enquiry_session ON nip_name_enquiries(session_id);

-- 3. NIP_AUDIT_TRAIL (APP Fraud Dispute Trail)
CREATE TABLE IF NOT EXISTS nip_audit_trail (
    audit_id BIGSERIAL PRIMARY KEY,
    session_id VARCHAR(128) NOT NULL,
    user_id UUID NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    ip_address VARCHAR(45) NOT NULL,
    device_info TEXT NOT NULL DEFAULT '',
    name_enquiry_result JSONB DEFAULT '{}',
    user_confirmed_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_nip_audit_session ON nip_audit_trail(session_id);
CREATE INDEX IF NOT EXISTS idx_nip_audit_user ON nip_audit_trail(user_id);
