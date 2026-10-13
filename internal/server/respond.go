package server

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Error codes are stable machine-readable strings alongside the human
// message: {"error":msg,"code":code}. Statuses stay frozen per condition
// (401/400/404/409/413/500 plus legacy 403/405/502/503).
func statusForCode(code string) int {
	switch code {
	case "login_required", "invalid_credentials", "auth_expired":
		return http.StatusUnauthorized
	case "csrf_required":
		return http.StatusForbidden
	case "bad_request", "bad_project", "rejected_upload":
		return http.StatusBadRequest
	case "label_taken":
		return http.StatusConflict
	case "unknown_site", "no_such_project", "no_such_favorite", "not_found":
		return http.StatusNotFound
	case "upload_too_large":
		return http.StatusRequestEntityTooLarge
	case "deploy_failed", "unknown":
		return http.StatusInternalServerError
	case "method_not_allowed":
		return http.StatusMethodNotAllowed
	case "auth_not_configured":
		return http.StatusServiceUnavailable
	case "verify_failed":
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

func writeErr(w http.ResponseWriter, code, msg string) {
	writeJSON(w, statusForCode(code), map[string]string{"error": msg, "code": code})
}
