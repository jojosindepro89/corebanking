import React, { useState, useEffect } from 'react';

const API_BASE = 'http://localhost:8081';

export default function App() {
  const [activeTab, setActiveTab] = useState('overview');
  const [isLightMode, setIsLightMode] = useState(false);
  const [authToken, setAuthToken] = useState(localStorage.getItem('tranzfa_jwt_token') || '');
  const [userEmail, setUserEmail] = useState('admin@bank.com');
  const [apiOnline, setApiOnline] = useState(true);

  // Data states
  const [overviewData, setOverviewData] = useState({ total_accounts: 12, total_deposits: '₦4,250,900,000', net_liquidity: '₦854,230,100' });
  const [reconcileHistory, setReconcileHistory] = useState([]);
  const [fraudHeld, setFraudHeld] = useState([]);
  const [kycQueue, setKycQueue] = useState([]);
  const [auditLogs, setAuditLogs] = useState([]);
  const [loading, setLoading] = useState(false);

  // Playground state
  const [pgEndpoint, setPgEndpoint] = useState('/api/v1/transfers');
  const [pgPayload, setPgPayload] = useState('{\n  "source_account_id": "00000000-0000-0000-0000-000000000000",\n  "destination_account_id": "00000000-0000-0000-0000-000000000000",\n  "amount_cents": 50000,\n  "description": "Transfer App Test"\n}');
  const [pgOutput, setPgOutput] = useState('// Result output will appear here...');

  // Account creation state
  const [newAccName, setNewAccName] = useState('');
  const [newAccCurrency, setNewAccCurrency] = useState('NGN');

  useEffect(() => {
    if (isLightMode) {
      document.body.classList.add('light-mode');
    } else {
      document.body.classList.remove('light-mode');
    }
  }, [isLightMode]);

  useEffect(() => {
    fetchHealth();
    if (activeTab === 'reconcile') fetchReconciliationHistory();
    if (activeTab === 'fraud') fetchFraudHeld();
    if (activeTab === 'kyc') fetchKYCQueue();
    if (activeTab === 'audit') fetchAuditLogs();
  }, [activeTab]);

  const apiFetch = async (endpoint, options = {}) => {
    options.headers = options.headers || {};
    if (authToken) {
      options.headers['Authorization'] = `Bearer ${authToken}`;
    }
    return fetch(`${API_BASE}${endpoint}`, options);
  };

  const fetchHealth = async () => {
    try {
      const res = await fetch(`${API_BASE}/health`);
      if (res.ok) setApiOnline(true);
    } catch {
      setApiOnline(false);
    }
  };

  const fetchReconciliationHistory = async () => {
    setLoading(true);
    try {
      const res = await apiFetch('/api/v1/admin/dashboard/reconcile/history');
      if (res.ok) {
        const data = await res.json();
        setReconcileHistory(data || []);
      }
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  const triggerReconciliation = async () => {
    try {
      const res = await apiFetch('/api/v1/admin/dashboard/reconcile/run', { method: 'POST' });
      const data = await res.json();
      alert(`Reconciliation Completed!\nAccounts Scanned: ${data.total_accounts}\nMismatches: ${data.mismatches_count}\nSystem Balanced: ${data.system_balanced}`);
      fetchReconciliationHistory();
    } catch (e) {
      alert(`Execution Error: ${e.message}`);
    }
  };

  const fetchFraudHeld = async () => {
    setLoading(true);
    try {
      const res = await apiFetch('/api/v1/admin/fraud/held');
      if (res.ok) {
        const data = await res.json();
        setFraudHeld(data || []);
      }
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  const fetchKYCQueue = async () => {
    setLoading(true);
    try {
      const res = await apiFetch('/api/v1/admin/dashboard/kyc/queue');
      if (res.ok) {
        const data = await res.json();
        setKycQueue(data || []);
      }
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  const fetchAuditLogs = async () => {
    setLoading(true);
    try {
      const res = await apiFetch('/api/v1/admin/audit-logs');
      if (res.ok) {
        const data = await res.json();
        setAuditLogs(data || []);
      }
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  const handlePlaygroundSubmit = async () => {
    setPgOutput('// Executing request against Core Banking API...');
    try {
      const payload = JSON.parse(pgPayload);
      const res = await apiFetch(pgEndpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
      });
      const data = await res.json();
      setPgOutput(`// Status Code: ${res.status}\n\n` + JSON.stringify(data, null, 2));
    } catch (err) {
      setPgOutput(`// Execution Failure: ${err.message}`);
    }
  };

  const handleCreateAccount = async () => {
    if (!newAccName) return alert('Please enter an account title.');
    try {
      const res = await apiFetch('/api/v1/accounts', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: newAccName, account_type: 'asset', currency: newAccCurrency })
      });
      const data = await res.json();
      if (res.ok) {
        alert(`Account Created Successfully!\nID: ${data.account_id}\nCurrency: ${data.currency}`);
        setNewAccName('');
      } else {
        alert(`Error: ${data.error || 'Failed to create account'}`);
      }
    } catch (e) {
      alert(`Error: ${e.message}`);
    }
  };

  const handleLogin = () => {
    const email = prompt('Enter Admin Email:', 'admin@bank.com');
    const password = prompt('Enter Password:', 'AdminPass123!');
    if (email && password) {
      fetch(`${API_BASE}/api/v1/auth/login`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email, password })
      })
        .then(r => r.json())
        .then(data => {
          if (data.access_token) {
            setAuthToken(data.access_token);
            localStorage.setItem('tranzfa_jwt_token', data.access_token);
            setUserEmail(email);
            alert('Authentication successful!');
          } else {
            alert('Authentication failed: ' + (data.error || 'Invalid credentials'));
          }
        })
        .catch(err => alert('Login Error: ' + err.message));
    }
  };

  return (
    <div style={{ display: 'flex', minHeight: '100vh', width: '100vw' }}>
      {/* Minimalist Sidebar with Transfer App Theme */}
      <aside style={{
        width: 'var(--sidebar-width)',
        background: 'rgba(7, 31, 23, 0.95)',
        borderRight: '1px solid var(--border-current)',
        backdropFilter: 'blur(20px)',
        display: 'flex',
        flexDirection: 'column',
        position: 'fixed',
        top: 0, bottom: 0, left: 0,
        zIndex: 100
      }}>
        <div style={{ padding: '1.5rem', borderBottom: '1px solid var(--border-current)' }}>
          <h1 style={{ fontSize: '1.25rem', fontWeight: 800, letterSpacing: '1px', color: '#FFFFFF' }}>
            TRANZFA <span style={{ color: 'var(--emerald-accent)' }}>CORE</span>
          </h1>
          <p style={{ fontSize: '0.65rem', fontWeight: 700, color: 'var(--emerald-accent)', letterSpacing: '2px', textTransform: 'uppercase', marginTop: '2px' }}>
            Transfer Banking Platform
          </p>
        </div>

        <nav style={{ padding: '1.25rem 0.75rem', display: 'flex', flexDirection: 'column', gap: '0.35rem', flex: 1, overflowY: 'auto' }}>
          {[
            { id: 'overview', label: 'Command Center' },
            { id: 'reconcile', label: 'Double-Entry Ledger' },
            { id: 'fraud', label: 'Fraud & Holds' },
            { id: 'kyc', label: 'KYC Verification' },
            { id: 'accounts', label: 'Virtual Accounts' },
            { id: 'cards', label: 'Card Issuance' },
            { id: 'playground', label: 'API Playground' },
            { id: 'audit', label: 'Audit Trail Logs' }
          ].map(item => (
            <button
              key={item.id}
              onClick={() => setActiveTab(item.id)}
              style={{
                display: 'flex',
                alignItems: 'center',
                padding: '0.75rem 1rem',
                color: activeTab === item.id ? 'var(--emerald-accent)' : 'var(--text-secondary-current)',
                background: activeTab === item.id ? 'rgba(0, 200, 140, 0.12)' : 'transparent',
                border: activeTab === item.id ? '1px solid rgba(0, 200, 140, 0.3)' : '1px solid transparent',
                borderRadius: '10px',
                fontWeight: activeTab === item.id ? 700 : 500,
                fontSize: '0.85rem',
                cursor: 'pointer',
                textAlign: 'left',
                transition: 'all 0.2s ease'
              }}
            >
              {item.label}
            </button>
          ))}
        </nav>

        <div style={{ padding: '1rem 1.25rem', borderTop: '1px solid var(--border-current)', fontSize: '0.7rem', fontWeight: 700, color: 'var(--text-secondary-current)', display: 'flex', justifyContent: 'space-between' }}>
          <span>SANDBOX MODE</span>
          <span style={{ color: 'var(--emerald-accent)' }}>REACT v18</span>
        </div>
      </aside>

      {/* Main Workspace */}
      <div style={{ marginLeft: 'var(--sidebar-width)', flex: 1, display: 'flex', flexDirection: 'column' }}>
        {/* Header Bar */}
        <header style={{
          height: 'var(--header-height)',
          background: 'rgba(4, 14, 10, 0.85)',
          backdropFilter: 'blur(20px)',
          borderBottom: '1px solid var(--border-current)',
          padding: '0 2rem',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          position: 'sticky',
          top: 0,
          zIndex: 90
        }}>
          <div>
            <h2 style={{ fontSize: '1.15rem', fontWeight: 800, textTransform: 'capitalize' }}>
              {activeTab === 'overview' ? 'Core Command Center' : activeTab.replace('_', ' ')}
            </h2>
            <p style={{ fontSize: '0.75rem', color: 'var(--text-secondary-current)' }}>
              Transfer App Core Banking Operations Architecture
            </p>
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: '1rem' }}>
            <button
              onClick={() => setIsLightMode(!isLightMode)}
              style={{
                background: 'rgba(0, 200, 140, 0.08)',
                border: '1px solid rgba(0, 200, 140, 0.3)',
                color: 'var(--emerald-accent)',
                padding: '0.4rem 0.85rem',
                borderRadius: '8px',
                fontSize: '0.78rem',
                fontWeight: 700,
                cursor: 'pointer'
              }}
            >
              {isLightMode ? 'Dark Theme' : 'Light Theme'}
            </button>

            <div style={{
              display: 'flex',
              alignItems: 'center',
              gap: '0.5rem',
              fontSize: '0.72rem',
              fontWeight: 700,
              padding: '0.35rem 0.85rem',
              borderRadius: '999px',
              background: apiOnline ? 'rgba(0, 200, 140, 0.12)' : 'rgba(239, 83, 29, 0.12)',
              color: apiOnline ? 'var(--emerald-accent)' : 'var(--orange-primary)',
              border: `1px solid ${apiOnline ? 'rgba(0, 200, 140, 0.3)' : 'rgba(239, 83, 29, 0.3)'}`
            }}>
              <span style={{
                width: '6px',
                height: '6px',
                borderRadius: '50%',
                background: apiOnline ? 'var(--emerald-accent)' : 'var(--orange-primary)'
              }} />
              <span>{apiOnline ? 'API OPERATIONAL' : 'OFFLINE'}</span>
            </div>

            <button
              onClick={handleLogin}
              style={{
                background: 'var(--dark-card)',
                border: '1px solid var(--border-current)',
                color: '#FFFFFF',
                padding: '0.45rem 0.9rem',
                borderRadius: '8px',
                fontSize: '0.8rem',
                fontWeight: 600,
                cursor: 'pointer'
              }}
            >
              {userEmail}
            </button>
          </div>
        </header>

        {/* Content Body */}
        <main style={{ padding: '2rem', flex: 1 }}>
          {/* OVERVIEW TAB */}
          {activeTab === 'overview' && (
            <div>
              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(240px, 1fr))', gap: '1.25rem', marginBottom: '1.75rem' }}>
                <div className="glass-box glow-emerald" style={{ padding: '1.4rem' }}>
                  <div style={{ fontSize: '0.72rem', fontWeight: 700, color: 'var(--text-secondary-current)', textTransform: 'uppercase', letterSpacing: '1px', marginBottom: '0.5rem' }}>
                    Active Enterprise Accounts
                  </div>
                  <div className="font-mono" style={{ fontSize: '1.8rem', fontWeight: 700, color: 'var(--emerald-accent)' }}>
                    {overviewData.total_accounts}
                  </div>
                  <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary-current)' }}>+3 new this month</div>
                </div>

                <div className="glass-box glow-gold" style={{ padding: '1.4rem' }}>
                  <div style={{ fontSize: '0.72rem', fontWeight: 700, color: 'var(--text-secondary-current)', textTransform: 'uppercase', letterSpacing: '1px', marginBottom: '0.5rem' }}>
                    Total Vault Deposits
                  </div>
                  <div className="font-mono" style={{ fontSize: '1.8rem', fontWeight: 700, color: 'var(--gold-primary)' }}>
                    {overviewData.total_deposits}
                  </div>
                  <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary-current)' }}>Fully Collateralized</div>
                </div>

                <div className="glass-box glow-orange" style={{ padding: '1.4rem' }}>
                  <div style={{ fontSize: '0.72rem', fontWeight: 700, color: 'var(--text-secondary-current)', textTransform: 'uppercase', letterSpacing: '1px', marginBottom: '0.5rem' }}>
                    Net Ledger Liquidity
                  </div>
                  <div className="font-mono" style={{ fontSize: '1.8rem', fontWeight: 700, color: 'var(--orange-primary)' }}>
                    {overviewData.net_liquidity}
                  </div>
                  <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary-current)' }}>Sub-minute Reconciliation</div>
                </div>
              </div>

              <div className="glass-box" style={{ padding: '1.5rem' }}>
                <h3 style={{ fontSize: '1rem', fontWeight: 700, marginBottom: '1rem' }}>Transfer Core Banking System Specifications</h3>
                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '1.5rem' }}>
                  <div>
                    <p style={{ fontSize: '0.88rem', color: 'var(--text-secondary-current)', marginBottom: '1rem' }}>
                      Built with high-concurrency Go ledger execution, deterministic row locking (`SELECT FOR UPDATE`), automated interest accruals, loan servicing, and instant interbank settlement.
                    </p>
                    <pre className="font-mono" style={{ background: '#020906', padding: '1.25rem', borderRadius: '10px', color: 'var(--emerald-accent)', fontSize: '0.82rem', border: '1px solid var(--border-current)' }}>
// Core Engine Architecture
- React Single Page App (Vite)
- Transfer Mobile App Theme Palette
- PostgreSQL Append-Only Immutability
- Double-Entry General Ledger Rules
- Real-Time Fraud & APP Evidence Queue
                    </pre>
                  </div>

                  <div>
                    <h4 style={{ fontSize: '0.85rem', color: 'var(--text-secondary-current)', marginBottom: '0.75rem', textTransform: 'uppercase' }}>Active System Services</h4>
                    <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.85rem', textAlign: 'left' }}>
                      <thead>
                        <tr style={{ borderBottom: '1px solid var(--border-current)', color: 'var(--text-secondary-current)' }}>
                          <th style={{ padding: '0.5rem' }}>Service</th>
                          <th style={{ padding: '0.5rem' }}>Status</th>
                        </tr>
                      </thead>
                      <tbody>
                        <tr style={{ borderBottom: '1px solid var(--border-current)' }}>
                          <td style={{ padding: '0.75rem 0.5rem' }}>API Gateway (Port 8081)</td>
                          <td style={{ padding: '0.75rem 0.5rem', color: 'var(--emerald-accent)', fontWeight: 700 }}>ONLINE</td>
                        </tr>
                        <tr style={{ borderBottom: '1px solid var(--border-current)' }}>
                          <td style={{ padding: '0.75rem 0.5rem' }}>Double-Entry Ledger</td>
                          <td style={{ padding: '0.75rem 0.5rem', color: 'var(--emerald-accent)', fontWeight: 700 }}>BALANCED</td>
                        </tr>
                        <tr style={{ borderBottom: '1px solid var(--border-current)' }}>
                          <td style={{ padding: '0.75rem 0.5rem' }}>React Dashboard (Vite)</td>
                          <td style={{ padding: '0.75rem 0.5rem', color: 'var(--emerald-accent)', fontWeight: 700 }}>ACTIVE</td>
                        </tr>
                      </tbody>
                    </table>
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* DOUBLE-ENTRY LEDGER RECONCILIATION TAB */}
          {activeTab === 'reconcile' && (
            <div className="glass-box" style={{ padding: '1.5rem' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.25rem' }}>
                <h3 style={{ fontSize: '1rem', fontWeight: 700 }}>Ledger Reconciliation History</h3>
                <button onClick={triggerReconciliation} style={{ background: 'var(--emerald-accent)', color: '#040E0A', border: 'none', padding: '0.65rem 1.2rem', borderRadius: '8px', fontWeight: 700, cursor: 'pointer' }}>
                  Execute Reconciliation Run
                </button>
              </div>

              {loading ? <p style={{ color: 'var(--text-secondary-current)' }}>Loading records...</p> : (
                <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.85rem', textAlign: 'left' }}>
                  <thead>
                    <tr style={{ borderBottom: '1px solid var(--border-current)', color: 'var(--text-secondary-current)', textTransform: 'uppercase', fontSize: '0.7rem' }}>
                      <th style={{ padding: '0.75rem 1rem' }}>Run ID</th>
                      <th style={{ padding: '0.75rem 1rem' }}>Accounts</th>
                      <th style={{ padding: '0.75rem 1rem' }}>Mismatches</th>
                      <th style={{ padding: '0.75rem 1rem' }}>Status</th>
                      <th style={{ padding: '0.75rem 1rem' }}>Timestamp</th>
                    </tr>
                  </thead>
                  <tbody>
                    {reconcileHistory.length === 0 ? (
                      <tr><td colSpan={5} style={{ padding: '1.5rem', textAlign: 'center', color: 'var(--text-secondary-current)' }}>No reconciliation records found.</td></tr>
                    ) : (
                      reconcileHistory.map(r => (
                        <tr key={r.run_id} style={{ borderBottom: '1px solid var(--border-current)' }}>
                          <td className="font-mono" style={{ padding: '0.85rem 1rem' }}>{r.run_id}</td>
                          <td className="font-mono" style={{ padding: '0.85rem 1rem' }}>{r.total_accounts}</td>
                          <td className="font-mono" style={{ padding: '0.85rem 1rem' }}>{r.mismatches_count}</td>
                          <td style={{ padding: '0.85rem 1rem' }}>
                            <span style={{ padding: '0.25rem 0.65rem', borderRadius: '4px', fontSize: '0.68rem', fontWeight: 700, background: r.system_balanced ? 'rgba(0, 200, 140, 0.12)' : 'rgba(239, 83, 29, 0.12)', color: r.system_balanced ? 'var(--emerald-accent)' : 'var(--orange-primary)' }}>
                              {r.system_balanced ? 'BALANCED' : 'IMBALANCED'}
                            </span>
                          </td>
                          <td style={{ padding: '0.85rem 1rem' }}>{new Date(r.created_at).toLocaleString()}</td>
                        </tr>
                      ))
                    )}
                  </tbody>
                </table>
              )}
            </div>
          )}

          {/* FRAUD & HOLDS TAB */}
          {activeTab === 'fraud' && (
            <div className="glass-box" style={{ padding: '1.5rem' }}>
              <h3 style={{ fontSize: '1rem', fontWeight: 700, marginBottom: '1rem' }}>High-Risk Held Transactions Queue</h3>
              {loading ? <p style={{ color: 'var(--text-secondary-current)' }}>Loading queue...</p> : (
                <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.85rem', textAlign: 'left' }}>
                  <thead>
                    <tr style={{ borderBottom: '1px solid var(--border-current)', color: 'var(--text-secondary-current)', textTransform: 'uppercase', fontSize: '0.7rem' }}>
                      <th style={{ padding: '0.75rem 1rem' }}>Hold ID</th>
                      <th style={{ padding: '0.75rem 1rem' }}>User ID</th>
                      <th style={{ padding: '0.75rem 1rem' }}>Amount</th>
                      <th style={{ padding: '0.75rem 1rem' }}>Status</th>
                    </tr>
                  </thead>
                  <tbody>
                    {fraudHeld.length === 0 ? (
                      <tr><td colSpan={4} style={{ padding: '1.5rem', textAlign: 'center', color: 'var(--text-secondary-current)' }}>No transactions currently pending risk review.</td></tr>
                    ) : (
                      fraudHeld.map(h => (
                        <tr key={h.hold_id} style={{ borderBottom: '1px solid var(--border-current)' }}>
                          <td className="font-mono" style={{ padding: '0.85rem 1rem' }}>{h.hold_id}</td>
                          <td className="font-mono" style={{ padding: '0.85rem 1rem' }}>{h.user_id}</td>
                          <td className="font-mono" style={{ padding: '0.85rem 1rem' }}>₦{(h.amount_cents / 100).toLocaleString()}</td>
                          <td style={{ padding: '0.85rem 1rem', color: 'var(--gold-primary)', fontWeight: 700 }}>{h.status}</td>
                        </tr>
                      ))
                    )}
                  </tbody>
                </table>
              )}
            </div>
          )}

          {/* KYC VERIFICATION TAB */}
          {activeTab === 'kyc' && (
            <div className="glass-box" style={{ padding: '1.5rem' }}>
              <h3 style={{ fontSize: '1rem', fontWeight: 700, marginBottom: '1rem' }}>Pending KYC Applications</h3>
              {loading ? <p style={{ color: 'var(--text-secondary-current)' }}>Loading queue...</p> : (
                <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.85rem', textAlign: 'left' }}>
                  <thead>
                    <tr style={{ borderBottom: '1px solid var(--border-current)', color: 'var(--text-secondary-current)', textTransform: 'uppercase', fontSize: '0.7rem' }}>
                      <th style={{ padding: '0.75rem 1rem' }}>Full Name</th>
                      <th style={{ padding: '0.75rem 1rem' }}>ID Number</th>
                      <th style={{ padding: '0.75rem 1rem' }}>DOB</th>
                      <th style={{ padding: '0.75rem 1rem' }}>Status</th>
                    </tr>
                  </thead>
                  <tbody>
                    {kycQueue.length === 0 ? (
                      <tr><td colSpan={4} style={{ padding: '1.5rem', textAlign: 'center', color: 'var(--text-secondary-current)' }}>No pending KYC applications in queue.</td></tr>
                    ) : (
                      kycQueue.map(k => (
                        <tr key={k.kyc_id} style={{ borderBottom: '1px solid var(--border-current)' }}>
                          <td style={{ padding: '0.85rem 1rem' }}>{k.full_name}</td>
                          <td className="font-mono" style={{ padding: '0.85rem 1rem' }}>{k.id_number}</td>
                          <td style={{ padding: '0.85rem 1rem' }}>{k.dob}</td>
                          <td style={{ padding: '0.85rem 1rem', color: 'var(--gold-primary)', fontWeight: 700 }}>{k.status}</td>
                        </tr>
                      ))
                    )}
                  </tbody>
                </table>
              )}
            </div>
          )}

          {/* VIRTUAL ACCOUNTS TAB */}
          {activeTab === 'accounts' && (
            <div className="glass-box" style={{ padding: '1.5rem' }}>
              <h3 style={{ fontSize: '1rem', fontWeight: 700, marginBottom: '1rem' }}>Issue Virtual Account</h3>
              <div style={{ maxWidth: '400px' }}>
                <div style={{ marginBottom: '1rem' }}>
                  <label style={{ display: 'block', fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-secondary-current)', marginBottom: '0.4rem' }}>Account Title</label>
                  <input
                    type="text"
                    value={newAccName}
                    onChange={e => setNewAccName(e.target.value)}
                    placeholder="e.g. Primary Savings Vault"
                    style={{ width: '100%', padding: '0.7rem 0.9rem', background: 'var(--dark-card)', border: '1px solid var(--border-current)', borderRadius: '8px', color: '#FFFFFF' }}
                  />
                </div>
                <div style={{ marginBottom: '1.25rem' }}>
                  <label style={{ display: 'block', fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-secondary-current)', marginBottom: '0.4rem' }}>Currency</label>
                  <select
                    value={newAccCurrency}
                    onChange={e => setNewAccCurrency(e.target.value)}
                    style={{ width: '100%', padding: '0.7rem 0.9rem', background: 'var(--dark-card)', border: '1px solid var(--border-current)', borderRadius: '8px', color: '#FFFFFF' }}
                  >
                    <option value="NGN">NGN - Nigerian Naira</option>
                    <option value="USD">USD - US Dollar</option>
                    <option value="EUR">EUR - Euro</option>
                  </select>
                </div>
                <button onClick={handleCreateAccount} style={{ background: 'var(--emerald-accent)', color: '#040E0A', border: 'none', padding: '0.65rem 1.2rem', borderRadius: '8px', fontWeight: 700, cursor: 'pointer' }}>
                  Generate Account
                </button>
              </div>
            </div>
          )}

          {/* CARD ISSUANCE TAB */}
          {activeTab === 'cards' && (
            <div className="glass-box" style={{ padding: '1.5rem' }}>
              <h3 style={{ fontSize: '1rem', fontWeight: 700, marginBottom: '0.5rem' }}>Virtual Card Issuance</h3>
              <p style={{ fontSize: '0.88rem', color: 'var(--text-secondary-current)', marginBottom: '1rem' }}>Issue virtual Mastercard/Visa debit cards for Transfer app users.</p>
              <button onClick={() => alert("Virtual Mastercard issued!\nPAN: 5399 8219 0491 8912\nCVV: 491 | EXP: 12/28")} style={{ background: 'var(--emerald-accent)', color: '#040E0A', border: 'none', padding: '0.65rem 1.2rem', borderRadius: '8px', fontWeight: 700, cursor: 'pointer' }}>
                Issue Virtual Card
              </button>
            </div>
          )}

          {/* API PLAYGROUND TAB */}
          {activeTab === 'playground' && (
            <div className="glass-box" style={{ padding: '1.5rem' }}>
              <h3 style={{ fontSize: '1rem', fontWeight: 700, marginBottom: '1rem' }}>Developer API Playground</h3>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '1.5rem' }}>
                <div>
                  <div style={{ marginBottom: '1rem' }}>
                    <label style={{ display: 'block', fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-secondary-current)', marginBottom: '0.4rem' }}>Endpoint</label>
                    <input
                      type="text"
                      value={pgEndpoint}
                      onChange={e => setPgEndpoint(e.target.value)}
                      style={{ width: '100%', padding: '0.7rem 0.9rem', background: 'var(--dark-card)', border: '1px solid var(--border-current)', borderRadius: '8px', color: '#FFFFFF' }}
                    />
                  </div>
                  <div style={{ marginBottom: '1rem' }}>
                    <label style={{ display: 'block', fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-secondary-current)', marginBottom: '0.4rem' }}>JSON Payload</label>
                    <textarea
                      rows={6}
                      value={pgPayload}
                      onChange={e => setPgPayload(e.target.value)}
                      style={{ width: '100%', padding: '0.7rem 0.9rem', background: 'var(--dark-card)', border: '1px solid var(--border-current)', borderRadius: '8px', color: '#FFFFFF', fontFamily: 'JetBrains Mono' }}
                    />
                  </div>
                  <button onClick={handlePlaygroundSubmit} style={{ background: 'var(--emerald-accent)', color: '#040E0A', border: 'none', padding: '0.65rem 1.2rem', borderRadius: '8px', fontWeight: 700, cursor: 'pointer' }}>
                    Dispatch Request
                  </button>
                </div>
                <div>
                  <label style={{ display: 'block', fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-secondary-current)', marginBottom: '0.4rem' }}>Response Output</label>
                  <pre className="font-mono" style={{ background: '#020906', padding: '1.25rem', borderRadius: '10px', color: 'var(--emerald-accent)', fontSize: '0.82rem', border: '1px solid var(--border-current)', overflowX: 'auto' }}>
                    {pgOutput}
                  </pre>
                </div>
              </div>
            </div>
          )}

          {/* AUDIT TRAIL TAB */}
          {activeTab === 'audit' && (
            <div className="glass-box" style={{ padding: '1.5rem' }}>
              <h3 style={{ fontSize: '1rem', fontWeight: 700, marginBottom: '1rem' }}>System Audit Logs</h3>
              {loading ? <p style={{ color: 'var(--text-secondary-current)' }}>Loading logs...</p> : (
                <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.85rem', textAlign: 'left' }}>
                  <thead>
                    <tr style={{ borderBottom: '1px solid var(--border-current)', color: 'var(--text-secondary-current)', textTransform: 'uppercase', fontSize: '0.7rem' }}>
                      <th style={{ padding: '0.75rem 1rem' }}>Log ID</th>
                      <th style={{ padding: '0.75rem 1rem' }}>Endpoint</th>
                      <th style={{ padding: '0.75rem 1rem' }}>Method</th>
                      <th style={{ padding: '0.75rem 1rem' }}>IP Address</th>
                      <th style={{ padding: '0.75rem 1rem' }}>Status</th>
                    </tr>
                  </thead>
                  <tbody>
                    {auditLogs.length === 0 ? (
                      <tr><td colSpan={5} style={{ padding: '1.5rem', textAlign: 'center', color: 'var(--text-secondary-current)' }}>No audit logs found.</td></tr>
                    ) : (
                      auditLogs.map(l => (
                        <tr key={l.log_id} style={{ borderBottom: '1px solid var(--border-current)' }}>
                          <td className="font-mono" style={{ padding: '0.85rem 1rem' }}>{l.log_id}</td>
                          <td className="font-mono" style={{ padding: '0.85rem 1rem' }}>{l.endpoint}</td>
                          <td className="font-mono" style={{ padding: '0.85rem 1rem' }}>{l.method}</td>
                          <td className="font-mono" style={{ padding: '0.85rem 1rem' }}>{l.ip_address}</td>
                          <td style={{ padding: '0.85rem 1rem', color: l.status_code < 400 ? 'var(--emerald-accent)' : 'var(--orange-primary)', fontWeight: 700 }}>{l.status_code}</td>
                        </tr>
                      ))
                    )}
                  </tbody>
                </table>
              )}
            </div>
          )}
        </main>
      </div>
    </div>
  );
}
