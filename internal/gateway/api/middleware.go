package api

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	gwdomain "core-banking-ledger/internal/gateway/domain"
	gwrepo "core-banking-ledger/internal/gateway/repository"
	gwservice "core-banking-ledger/internal/gateway/service"

	"github.com/google/uuid"
)

type contextKey string

const (
	UserIDKey    contextKey = "user_id"
	EmailKey     contextKey = "email"
	RoleKey      contextKey = "role"
	MFACodeKey   contextKey = "mfa_code"
	AccountIDKey contextKey = "account_id"
)

func AuthMiddleware(authSvc *gwservice.AuthService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing Authorization header"})
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid Authorization header format"})
				return
			}

			claims, err := authSvc.ParseAccessToken(parts[1])
			if err != nil {
				respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
				return
			}

			ctx := context.WithValue(r.Context(), UserIDKey, claims.UserID)
			ctx = context.WithValue(ctx, EmailKey, claims.Email)
			ctx = context.WithValue(ctx, RoleKey, claims.Role)
			if rw, ok := w.(*responseWriterInterceptor); ok {
				rw.finalCtx = ctx
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func AdminMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role, _ := r.Context().Value(RoleKey).(string)
			if role != string(gwdomain.RoleAdmin) {
				respondJSON(w, http.StatusForbidden, map[string]string{"error": "admin privileges required"})
				return
			}
			next.ServeHTTP(w, r.WithContext(ctxWithMFAHeader(r)))
		})
	}
}

func MFAHeaderExtractor() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mfaCode := r.Header.Get("X-MFA-Code")
			ctx := context.WithValue(r.Context(), MFACodeKey, mfaCode)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func ctxWithMFAHeader(r *http.Request) context.Context {
	mfaCode := r.Header.Get("X-MFA-Code")
	return context.WithValue(r.Context(), MFACodeKey, mfaCode)
}

// responseWriterInterceptor captures HTTP status code and final request context for audit logging
type responseWriterInterceptor struct {
	http.ResponseWriter
	statusCode int
	finalCtx   context.Context
}

func (w *responseWriterInterceptor) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func APIAuditLoggingMiddleware(repo *gwrepo.GatewayRepository) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := &responseWriterInterceptor{ResponseWriter: w, statusCode: http.StatusOK, finalCtx: r.Context()}
			startTime := time.Now().UTC()

			next.ServeHTTP(rw, r)

			// Asynchronous audit record insertion using final captured context
			go func(req *http.Request, status int, reqCtx context.Context, t time.Time) {
				if reqCtx == nil {
					reqCtx = req.Context()
				}
				var userID *uuid.UUID
				if val, ok := reqCtx.Value(UserIDKey).(string); ok && val != "" {
					if parsed, err := uuid.Parse(val); err == nil {
						userID = &parsed
					}
				}

				var accountID *uuid.UUID
				if val, ok := reqCtx.Value(AccountIDKey).(string); ok && val != "" {
					if parsed, err := uuid.Parse(val); err == nil {
						accountID = &parsed
					}
				}

				ip := getClientIP(req)
				outcome := "SUCCESS"
				if status >= 400 {
					outcome = "FAILURE"
				}

				auditLog := &gwdomain.APIAuditRecord{
					UserID:     userID,
					Endpoint:   req.URL.Path,
					Method:     req.Method,
					AccountID:  accountID,
					IPAddress:  ip,
					StatusCode: status,
					Outcome:    outcome,
					CreatedAt:  t,
				}

				_ = repo.CreateAPIAuditLog(context.Background(), repo.DB(), auditLog)
			}(r, rw.statusCode, rw.finalCtx, startTime)
		})
	}
}

func getClientIP(r *http.Request) string {
	ip := r.Header.Get("X-Forwarded-For")
	if ip != "" {
		parts := strings.Split(ip, ",")
		return strings.TrimSpace(parts[0])
	}
	ip = r.Header.Get("X-Real-IP")
	if ip != "" {
		return strings.TrimSpace(ip)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func CORSMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	originsMap := make(map[string]bool)
	for _, o := range allowedOrigins {
		originsMap[o] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && (originsMap[origin] || originsMap["*"]) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-MFA-Code, X-Idempotency-Key")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func SecurityHeadersMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("X-XSS-Protection", "1; mode=block")
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			next.ServeHTTP(w, r)
		})
	}
}

// BodyReader helper to copy request body without draining it
func drainBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	buf, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewBuffer(buf))
	return buf
}
