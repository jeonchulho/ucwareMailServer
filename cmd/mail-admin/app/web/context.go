package web

import "context"

func setCtxEmail(ctx context.Context, email string) context.Context {
	return context.WithValue(ctx, ctxEmail, email)
}

func setCtxRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, ctxRole, role)
}

func ctxEmailFrom(r interface{ Context() context.Context }) string {
	v, _ := r.Context().Value(ctxEmail).(string)
	return v
}

func ctxRoleFrom(r interface{ Context() context.Context }) string {
	v, _ := r.Context().Value(ctxRole).(string)
	return v
}
