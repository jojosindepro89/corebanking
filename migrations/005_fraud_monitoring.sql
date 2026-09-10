-- 005_fraud_monitoring.sql: Schema for Real-Time Fraud Detection, APP Dispute Evidence, Device History, and Dynamic Rule Configurations

-- 1. FRAUD_RULE_CONFIGS (Dynamic Threshold Settings)
CREATE TABLE IF NOT EXISTS fraud_rule_configs (
    rule_key VARCHAR(100) PRIMARY KEY,
    value_numeric NUMERIC NOT NULL DEFAULT 0,
    value_string VARCHAR(255) NOT NULL DEFAULT '',
    is_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    description TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed Initial Threshold Defaults
INSERT INTO fraud_rule_configs (rule_key, value_numeric, description)
VALUES 
    ('velocity_max_transfers_per_hour', 5, 'Maximum allowed transfers per hour before velocity risk flag'),
    ('velocity_max_transfers_per_day', 10, 'Maximum allowed transfers per 24 hours'),
    ('amount_anomaly_multiplier', 5.0, 'Multiplier over trailing 30-day average transfer size'),
    ('new_recipient_high_amount_cents', 200000, 'Threshold in cents ($2,000.00) for first-ever transfer to recipient'),
    ('rapid_tier_limit_threshold_ratio', 0.8, 'Ratio of new KYC tier limit transacted within 48h')
ON CONFLICT (rule_key) DO NOTHING;

-- 2. USER_DEVICES (Fingerprinting & Location tracking)
CREATE TABLE IF NOT EXISTS user_devices (
    device_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    device_fingerprint VARCHAR(255) NOT NULL,
    last_ip VARCHAR(45) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, device_fingerprint)
);

CREATE INDEX IF NOT EXISTS idx_user_devices_user ON user_devices(user_id);

-- 3. FRAUD_EVIDENCE_RECORDS (Immutable APP Fraud Dispute Paper Trail)
CREATE TABLE IF NOT EXISTS fraud_evidence_records (
    evidence_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id UUID REFERENCES transactions(transaction_id) ON DELETE SET NULL,
    user_id UUID NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    session_id VARCHAR(128) NOT NULL DEFAULT '',
    name_enquiry_result JSONB DEFAULT '{}',
    user_confirmed_at TIMESTAMPTZ NOT NULL,
    device_fingerprint VARCHAR(255) NOT NULL DEFAULT '',
    ip_address VARCHAR(45) NOT NULL DEFAULT '',
    risk_score VARCHAR(20) NOT NULL CHECK (risk_score IN ('LOW', 'MEDIUM', 'HIGH')),
    rules_triggered JSONB DEFAULT '[]',
    stepup_required BOOLEAN NOT NULL DEFAULT FALSE,
    stepup_completed BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Index for Sub-minute Dispute Retrieval Mandate
CREATE INDEX IF NOT EXISTS idx_fraud_evidence_tx_id ON fraud_evidence_records(transaction_id);
CREATE INDEX IF NOT EXISTS idx_fraud_evidence_user_id ON fraud_evidence_records(user_id);

-- 4. HELD_FRAUD_TRANSACTIONS (High-Risk Manual Hold Queue)
CREATE TABLE IF NOT EXISTS held_fraud_transactions (
    hold_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id UUID REFERENCES transactions(transaction_id) ON DELETE SET NULL,
    user_id UUID NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    evidence_id UUID NOT NULL REFERENCES fraud_evidence_records(evidence_id) ON DELETE CASCADE,
    amount_cents BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(50) NOT NULL DEFAULT 'pending_review' CHECK (status IN ('pending_review', 'approved', 'rejected')),
    reviewed_by UUID REFERENCES users(user_id) ON DELETE SET NULL,
    rejection_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_held_fraud_status ON held_fraud_transactions(status);
