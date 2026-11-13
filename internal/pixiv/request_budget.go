package pixiv

import "context"

type additionalRequestBudgetKey struct{}
type additionalRequestBudgetError struct{ err error }

func (e *additionalRequestBudgetError) Error() string { return e.err.Error() }
func (e *additionalRequestBudgetError) Unwrap() error { return e.err }

// WithAdditionalRequestBudget reserves requests beyond the caller's initial API reservation.
// A nil callback leaves the client behavior unchanged.
func WithAdditionalRequestBudget(ctx context.Context, reserve func(context.Context) error) context.Context {
	if reserve == nil {
		return ctx
	}
	return context.WithValue(ctx, additionalRequestBudgetKey{}, reserve)
}

func reserveAdditionalRequest(ctx context.Context) error {
	if reserve, ok := ctx.Value(additionalRequestBudgetKey{}).(func(context.Context) error); ok {
		if err := reserve(ctx); err != nil {
			return &additionalRequestBudgetError{err}
		}
	}
	return nil
}
