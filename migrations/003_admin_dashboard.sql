-- 003_admin_dashboard.sql: Schema for Admin Dashboard, Reconciliation Runs, Flagged Transactions & KYC Queue

-- 1. RECONCILIATION_RUNS
CREATE TABLE IF NOT EXISTS reconciliation_runs (
    run_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    total_accounts INT NOT NULL DEFAULT 0,
    mismatches_count INT NOT NULL DEFAULT 0,
    system_balanced BOOLEAN NOT NULL DEFAULT TRUE,
    discrepancies JSONB DEFAULT '[]',
    triggered_by VARCHAR(50) NOT NULL DEFAULT 'scheduled',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_reconciliation_runs_created ON reconciliation_runs(created_at DESC);

-- 2. FLAGGED_TRANSACTIONS (High-value / Fraud Monitoring Queue)
CREATE TABLE IF NOT EXISTS flagged_transactions (
    flag_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id UUID NOT NULL REFERENCES transactions(transaction_id) ON DELETE CASCADE,
    amount_cents BIGINT NOT NULL,
    rule_triggered VARCHAR(255) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'pending_review' CHECK (status IN ('pending_review', 'approved', 'dismissed')),
    reviewed_by UUID REFERENCES users(user_id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_flagged_tx_status ON flagged_transactions(status);
CREATE INDEX IF NOT EXISTS idx_flagged_tx_created ON flagged_transactions(created_at DESC);

-- 3. KYC_APPLICATIONS (Manual Review Queue)
CREATE TABLE IF NOT EXISTS kyc_applications (
    kyc_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    full_name VARCHAR(255) NOT NULL,
    dob VARCHAR(50) NOT NULL,
    id_number VARCHAR(100) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'pending_review' CHECK (status IN ('pending_review', 'approved', 'rejected')),
    reviewed_by UUID REFERENCES users(user_id) ON DELETE SET NULL,
    rejection_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_kyc_status ON kyc_applications(status);
CREATE INDEX IF NOT EXISTS idx_kyc_user ON kyc_applications(user_id);
