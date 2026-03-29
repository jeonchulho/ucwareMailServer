package security

import (
	"net"
	"strings"

	"github.com/jeonchulho/ucwareMailServer/cmd/mail-admin/app/internal/store"
)

const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

func ActorFromEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func IsValidRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case RoleAdmin, RoleOperator, RoleViewer:
		return true
	default:
		return false
	}
}

func RemoteIP(remoteAddr string) string {
	ip := strings.TrimSpace(remoteAddr)
	if host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr)); err == nil {
		ip = host
	}
	return ip
}

func BuildAuditLog(action, actor, email, status, message, remoteAddr, userAgent string) store.AuditLog {
	return store.AuditLog{
		Action:    action,
		Actor:     ActorFromEmail(actor),
		Email:     ActorFromEmail(email),
		Status:    status,
		Message:   message,
		RemoteIP:  RemoteIP(remoteAddr),
		UserAgent: userAgent,
	}
}
