package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	gwdomain "core-banking-ledger/internal/gateway/domain"

	"github.com/google/uuid"
)

func TestAuth_PasswordComplexityEnforcement(t *testing.T) {
	_, router, _, gService := setupGatewayTest(t)
	ctx := context.Background()

	weakPasswords := []string{
		"short",        // < 8 chars
		"alllowercase", // missing upper, digit, special
		"ALLUPPERCASE", // missing lower, digit, special
		"NoSpecial123", // missing special char
		"NoDigits!@#",  // missing digit
	}

	for _, wp := range weakPasswords {
		email := fmt.Sprintf("weak_%s@example.com", uuid.New().String()[:8])
		_, err := gService.RegisterUser(ctx, gwdomain.RegisterRequest{Email: email, Password: wp})
		if err == nil {
			t.Errorf("Expected registration failure for weak password %q, but succeeded", wp)
		}
	}

	// Valid Password
	validEmail := fmt.Sprintf("valid_%s@example.com", uuid.New().String()[:8])
	regBody, _ := json.Marshal(gwdomain.RegisterRequest{Email: validEmail, Password: "StrongPass123!"})
	req := httptest.NewRequest("POST", "/auth/register", bytes.NewBuffer(regBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("Expected 201 Created for valid password, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuth_AccountLockoutAfterFiveFailedAttempts(t *testing.T) {
	_, router, _, gService := setupGatewayTest(t)
	ctx := context.Background()

	email := fmt.Sprintf("lockout_%s@example.com", uuid.New().String()[:8])
	password := "CorrectPass123!"

	_, err := gService.RegisterUser(ctx, gwdomain.RegisterRequest{Email: email, Password: password})
	if err != nil {
		t.Fatalf("RegisterUser failed: %v", err)
	}

	// Fail 4 login attempts
	for i := 1; i <= 4; i++ {
		loginBody, _ := json.Marshal(gwdomain.LoginRequest{Email: email, Password: "WrongPassword123!"})
		req := httptest.NewRequest("POST", "/auth/login", bytes.NewBuffer(loginBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("Attempt %d: Expected 401 Unauthorized, got %d", i, w.Code)
		}
	}

	// 5th Failed attempt -> Account Lockout
	loginBody5, _ := json.Marshal(gwdomain.LoginRequest{Email: email, Password: "WrongPassword123!"})
	req5 := httptest.NewRequest("POST", "/auth/login", bytes.NewBuffer(loginBody5))
	req5.Header.Set("Content-Type", "application/json")
	w5 := httptest.NewRecorder()
	router.ServeHTTP(w5, req5)

	if w5.Code != http.StatusLocked {
		t.Fatalf("Attempt 5: Expected 423 StatusLocked, got %d: %s", w5.Code, w5.Body.String())
	}

	// Attempt login with CORRECT password while account is locked -> Must STILL be rejected with HTTP 423
	loginBodyCorrect, _ := json.Marshal(gwdomain.LoginRequest{Email: email, Password: password})
	reqCorrect := httptest.NewRequest("POST", "/auth/login", bytes.NewBuffer(loginBodyCorrect))
	reqCorrect.Header.Set("Content-Type", "application/json")
	wCorrect := httptest.NewRecorder()
	router.ServeHTTP(wCorrect, reqCorrect)

	if wCorrect.Code != http.StatusLocked {
		t.Errorf("Expected 423 StatusLocked even with correct password during lockout, got %d", wCorrect.Code)
	}
}

func TestAuth_LogoutAndLogoutAll(t *testing.T) {
	_, router, _, gService := setupGatewayTest(t)
	ctx := context.Background()

	email := fmt.Sprintf("logout_%s@example.com", uuid.New().String()[:8])
	password := "Password123!"

	_, _ = gService.RegisterUser(ctx, gwdomain.RegisterRequest{Email: email, Password: password})
	loginResp, err := gService.LoginUser(ctx, gwdomain.LoginRequest{Email: email, Password: password})
	if err != nil {
		t.Fatalf("LoginUser failed: %v", err)
	}

	// Single Logout
	logoutBody, _ := json.Marshal(gwdomain.LogoutRequest{RefreshToken: loginResp.RefreshToken})
	reqLogout := httptest.NewRequest("POST", "/auth/logout", bytes.NewBuffer(logoutBody))
	reqLogout.Header.Set("Content-Type", "application/json")
	wLogout := httptest.NewRecorder()
	router.ServeHTTP(wLogout, reqLogout)

	if wLogout.Code != http.StatusOK {
		t.Errorf("Expected 200 OK on logout, got %d", wLogout.Code)
	}

	// Verify Refresh Token is now revoked
	refBody, _ := json.Marshal(gwdomain.RefreshTokenRequest{RefreshToken: loginResp.RefreshToken})
	reqRef := httptest.NewRequest("POST", "/auth/refresh", bytes.NewBuffer(refBody))
	reqRef.Header.Set("Content-Type", "application/json")
	wRef := httptest.NewRecorder()
	router.ServeHTTP(wRef, reqRef)

	if wRef.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized for refreshed revoked token, got %d", wRef.Code)
	}

	// Login twice to create 2 active session families
	loginA, _ := gService.LoginUser(ctx, gwdomain.LoginRequest{Email: email, Password: password})
	loginB, _ := gService.LoginUser(ctx, gwdomain.LoginRequest{Email: email, Password: password})

	// Perform Logout All (Revoke All Sessions)
	reqLogoutAll := httptest.NewRequest("POST", "/auth/logout-all", nil)
	reqLogoutAll.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginA.AccessToken))
	wLogoutAll := httptest.NewRecorder()
	router.ServeHTTP(wLogoutAll, reqLogoutAll)

	if wLogoutAll.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on logout-all, got %d", wLogoutAll.Code)
	}

	// Both refresh tokens must now be rejected
	for i, ref := range []string{loginA.RefreshToken, loginB.RefreshToken} {
		refPayload, _ := json.Marshal(gwdomain.RefreshTokenRequest{RefreshToken: ref})
		reqTest := httptest.NewRequest("POST", "/auth/refresh", bytes.NewBuffer(refPayload))
		reqTest.Header.Set("Content-Type", "application/json")
		wTest := httptest.NewRecorder()
		router.ServeHTTP(wTest, reqTest)

		if wTest.Code != http.StatusUnauthorized {
			t.Errorf("Session %d: Expected 401 Unauthorized after LogoutAll, got %d", i+1, wTest.Code)
		}
	}
}

func TestAuth_ChangePassword(t *testing.T) {
	_, router, _, gService := setupGatewayTest(t)
	ctx := context.Background()

	email := fmt.Sprintf("chgpass_%s@example.com", uuid.New().String()[:8])
	oldPassword := "OldPassword123!"
	newPassword := "NewPassword456!"

	_, _ = gService.RegisterUser(ctx, gwdomain.RegisterRequest{Email: email, Password: oldPassword})
	loginResp, _ := gService.LoginUser(ctx, gwdomain.LoginRequest{Email: email, Password: oldPassword})

	// Change Password
	chgBody, _ := json.Marshal(gwdomain.ChangePasswordRequest{
		OldPassword: oldPassword,
		NewPassword: newPassword,
	})
	reqChg := httptest.NewRequest("POST", "/auth/password/change", bytes.NewBuffer(chgBody))
	reqChg.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginResp.AccessToken))
	reqChg.Header.Set("Content-Type", "application/json")
	wChg := httptest.NewRecorder()

	router.ServeHTTP(wChg, reqChg)
	if wChg.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on password change, got %d: %s", wChg.Code, wChg.Body.String())
	}

	// Old password login fails
	loginOld, _ := json.Marshal(gwdomain.LoginRequest{Email: email, Password: oldPassword})
	reqOld := httptest.NewRequest("POST", "/auth/login", bytes.NewBuffer(loginOld))
	reqOld.Header.Set("Content-Type", "application/json")
	wOld := httptest.NewRecorder()
	router.ServeHTTP(wOld, reqOld)

	if wOld.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 for old password after change, got %d", wOld.Code)
	}

	// New password login succeeds
	loginNew, _ := json.Marshal(gwdomain.LoginRequest{Email: email, Password: newPassword})
	reqNew := httptest.NewRequest("POST", "/auth/login", bytes.NewBuffer(loginNew))
	reqNew.Header.Set("Content-Type", "application/json")
	wNew := httptest.NewRecorder()
	router.ServeHTTP(wNew, reqNew)

	if wNew.Code != http.StatusOK {
		t.Errorf("Expected 200 OK for new password, got %d", wNew.Code)
	}
}

func TestAuth_TransactionPINSetupVerificationAndLockout(t *testing.T) {
	_, router, _, gService := setupGatewayTest(t)
	ctx := context.Background()

	email := fmt.Sprintf("pin_%s@example.com", uuid.New().String()[:8])
	password := "Password123!"

	_, _ = gService.RegisterUser(ctx, gwdomain.RegisterRequest{Email: email, Password: password})
	loginResp, _ := gService.LoginUser(ctx, gwdomain.LoginRequest{Email: email, Password: password})

	// 1. Verify PIN before setup -> Should return 401 ErrPINNotSetup
	verBody, _ := json.Marshal(gwdomain.VerifyPINRequest{PIN: "1234"})
	reqVerPre := httptest.NewRequest("POST", "/auth/pin/verify", bytes.NewBuffer(verBody))
	reqVerPre.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginResp.AccessToken))
	reqVerPre.Header.Set("Content-Type", "application/json")
	wVerPre := httptest.NewRecorder()
	router.ServeHTTP(wVerPre, reqVerPre)

	if wVerPre.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 for uninitialized PIN verification, got %d", wVerPre.Code)
	}

	// 2. Invalid PIN Format Setup (< 4 digits) -> Should fail
	invalidPinBody, _ := json.Marshal(gwdomain.SetupPINRequest{PIN: "12"})
	reqInv := httptest.NewRequest("POST", "/auth/pin/setup", bytes.NewBuffer(invalidPinBody))
	reqInv.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginResp.AccessToken))
	reqInv.Header.Set("Content-Type", "application/json")
	wInv := httptest.NewRecorder()
	router.ServeHTTP(wInv, reqInv)

	if wInv.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request for invalid PIN length, got %d", wInv.Code)
	}

	// 3. Valid PIN Setup (4 digits: "5678")
	setupBody, _ := json.Marshal(gwdomain.SetupPINRequest{PIN: "5678"})
	reqSetup := httptest.NewRequest("POST", "/auth/pin/setup", bytes.NewBuffer(setupBody))
	reqSetup.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginResp.AccessToken))
	reqSetup.Header.Set("Content-Type", "application/json")
	wSetup := httptest.NewRecorder()
	router.ServeHTTP(wSetup, reqSetup)

	if wSetup.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on PIN setup, got %d: %s", wSetup.Code, wSetup.Body.String())
	}

	// 4. Verify Correct PIN
	verValidBody, _ := json.Marshal(gwdomain.VerifyPINRequest{PIN: "5678"})
	reqVal := httptest.NewRequest("POST", "/auth/pin/verify", bytes.NewBuffer(verValidBody))
	reqVal.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginResp.AccessToken))
	reqVal.Header.Set("Content-Type", "application/json")
	wVal := httptest.NewRecorder()
	router.ServeHTTP(wVal, reqVal)

	if wVal.Code != http.StatusOK {
		t.Errorf("Expected 200 OK for valid PIN verification, got %d", wVal.Code)
	}

	// 5. Fail 2 PIN attempts
	for i := 1; i <= 2; i++ {
		wrongBody, _ := json.Marshal(gwdomain.VerifyPINRequest{PIN: "0000"})
		reqWr := httptest.NewRequest("POST", "/auth/pin/verify", bytes.NewBuffer(wrongBody))
		reqWr.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginResp.AccessToken))
		reqWr.Header.Set("Content-Type", "application/json")
		wWr := httptest.NewRecorder()
		router.ServeHTTP(wWr, reqWr)

		if wWr.Code != http.StatusUnauthorized {
			t.Errorf("PIN Attempt %d: Expected 401 Unauthorized, got %d", i, wWr.Code)
		}
	}

	// 6. 3rd Failed PIN attempt -> PIN Lockout
	wrongBody3, _ := json.Marshal(gwdomain.VerifyPINRequest{PIN: "0000"})
	reqWr3 := httptest.NewRequest("POST", "/auth/pin/verify", bytes.NewBuffer(wrongBody3))
	reqWr3.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginResp.AccessToken))
	reqWr3.Header.Set("Content-Type", "application/json")
	wWr3 := httptest.NewRecorder()
	router.ServeHTTP(wWr3, reqWr3)

	if wWr3.Code != http.StatusUnauthorized {
		t.Errorf("PIN Attempt 3: Expected 401 Unauthorized for PIN lockout, got %d", wWr3.Code)
	}

	// Subsequent PIN check with CORRECT PIN during lockout -> Must fail
	reqLockVal := httptest.NewRequest("POST", "/auth/pin/verify", bytes.NewBuffer(verValidBody))
	reqLockVal.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginResp.AccessToken))
	reqLockVal.Header.Set("Content-Type", "application/json")
	wLockVal := httptest.NewRecorder()
	router.ServeHTTP(wLockVal, reqLockVal)

	if wLockVal.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized for PIN verification during lockout, got %d", wLockVal.Code)
	}
}
