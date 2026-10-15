package domain

import "context"

type Account struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	PixivID      int64  `json:"pixiv_id,string"`
	TokenFile    string `json:"-"`
	Status       string `json:"status"`
	SyncState    string `json:"sync_state"`
	LastSyncedAt int64  `json:"last_synced_at"`
	ErrorCode    string `json:"error_code"`
}
type accountContextKey struct{}

func WithAccount(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, accountContextKey{}, id)
}
func ContextAccount(ctx context.Context) string {
	id, _ := ctx.Value(accountContextKey{}).(string)
	return id
}
