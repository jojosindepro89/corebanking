package nip

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

type TransferStatus string

const (
	StatusSuccess        TransferStatus = "SUCCESS"
	StatusFailed         TransferStatus = "FAILED"
	StatusPendingTimeout TransferStatus = "PENDING_TIMEOUT"
)

type NIBSSResponse struct {
	ResponseCode   string         `json:"response_code"`
	SessionID      string         `json:"session_id"`
	TransactionRef string         `json:"transaction_ref"`
	Status         TransferStatus `json:"status"`
	Message        string         `json:"message"`
}

type SingleTransferRequest struct {
	SessionID                 string `json:"session_id"`
	TransactionRef            string `json:"transaction_ref"`
	SourceBankCode            string `json:"source_bank_code"`
	SourceAccountNumber       string `json:"source_account_number"`
	SourceAccountName         string `json:"source_account_name"`
	DestinationBankCode       string `json:"destination_bank_code"`
	DestinationAccountNumber  string `json:"destination_account_number"`
	DestinationAccountName    string `json:"destination_account_name"`
	AmountCents               int64  `json:"amount_cents"`
	PaymentReference          string `json:"payment_reference"`
}

type NIBSSProvider interface {
	NameEnquiry(ctx context.Context, bankCode, accountNumber string) (accountName string, sessionID string, responseCode string, err error)
	SingleTransfer(ctx context.Context, req SingleTransferRequest) (*NIBSSResponse, error)
	QueryTransactionStatus(ctx context.Context, sessionID, txRef string) (*NIBSSResponse, error)
}

// MockNIBSSProvider simulates NIBSS interbank network behavior for certification & testing
type MockNIBSSProvider struct {
	mu                sync.RWMutex
	SimulateTimeout   bool
	SimulateFailure   bool
	statuses          map[string]TransferStatus // session_id -> status
}

func NewMockNIBSSProvider() *MockNIBSSProvider {
	return &MockNIBSSProvider{
		statuses: make(map[string]TransferStatus),
	}
}

func (m *MockNIBSSProvider) SetSimulateTimeout(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SimulateTimeout = v
}

func (m *MockNIBSSProvider) SetSimulateFailure(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SimulateFailure = v
}

func (m *MockNIBSSProvider) NameEnquiry(ctx context.Context, bankCode, accountNumber string) (string, string, string, error) {
	if len(accountNumber) != 10 {
		return "", "", "12", fmt.Errorf("invalid account number length (NIP requires 10 digits)")
	}

	sessionID := fmt.Sprintf("999%d%06d", time.Now().UnixNano()/1e6, rand.Intn(1000000))
	resolvedName := fmt.Sprintf("CHIEF ADEBAYO %s", accountNumber[6:])
	return resolvedName, sessionID, "00", nil
}

func (m *MockNIBSSProvider) SingleTransfer(ctx context.Context, req SingleTransferRequest) (*NIBSSResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.SimulateTimeout {
		m.statuses[req.SessionID] = StatusPendingTimeout
		return &NIBSSResponse{
			ResponseCode:   "96",
			SessionID:      req.SessionID,
			TransactionRef: req.TransactionRef,
			Status:         StatusPendingTimeout,
			Message:        "NIBSS Network Timeout / System Unavailable",
		}, nil
	}

	if m.SimulateFailure {
		m.statuses[req.SessionID] = StatusFailed
		return &NIBSSResponse{
			ResponseCode:   "99",
			SessionID:      req.SessionID,
			TransactionRef: req.TransactionRef,
			Status:         StatusFailed,
			Message:        "Destination Bank Account Blocked or Inactive",
		}, nil
	}

	m.statuses[req.SessionID] = StatusSuccess
	return &NIBSSResponse{
		ResponseCode:   "00",
		SessionID:      req.SessionID,
		TransactionRef: req.TransactionRef,
		Status:         StatusSuccess,
		Message:        "Approved or Completed Successfully",
	}, nil
}

func (m *MockNIBSSProvider) QueryTransactionStatus(ctx context.Context, sessionID, txRef string) (*NIBSSResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status, exists := m.statuses[sessionID]
	if !exists {
		status = StatusSuccess
	}

	code := "00"
	msg := "Approved or Completed Successfully"
	if status == StatusFailed {
		code = "99"
		msg = "Failed"
	} else if status == StatusPendingTimeout {
		code = "96"
		msg = "Pending NIBSS Resolution"
	}

	return &NIBSSResponse{
		ResponseCode:   code,
		SessionID:      sessionID,
		TransactionRef: txRef,
		Status:         status,
		Message:        msg,
	}, nil
}

func (m *MockNIBSSProvider) SetStatusForSession(sessionID string, status TransferStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses[sessionID] = status
}

func (m *MockNIBSSProvider) SetMode(sessionID string, mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch mode {
	case "timeout":
		m.statuses[sessionID] = StatusPendingTimeout
		m.SimulateTimeout = true
	case "success":
		m.statuses[sessionID] = StatusSuccess
		m.SimulateTimeout = false
		m.SimulateFailure = false
	case "failure":
		m.statuses[sessionID] = StatusFailed
		m.SimulateFailure = true
	}
}
