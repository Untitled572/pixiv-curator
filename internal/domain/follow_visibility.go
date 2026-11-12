package domain

import "context"

type followVisibilityContextKey struct{}

func WithFollowVisibility(ctx context.Context, visibility string) context.Context {
	return context.WithValue(ctx, followVisibilityContextKey{}, visibility)
}
func ContextFollowVisibility(ctx context.Context) string {
	if value, ok := ctx.Value(followVisibilityContextKey{}).(string); ok && value != "" {
		return value
	}
	return "public"
}
func ValidFollowVisibility(value string) bool { return value == "public" || value == "private" }
