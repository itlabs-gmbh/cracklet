package broker

import "context"

type injectedKey struct{}

func withInjected(ctx context.Context, headers map[string]string) context.Context {
	return context.WithValue(ctx, injectedKey{}, headers)
}

func injectedFrom(ctx context.Context) map[string]string {
	h, _ := ctx.Value(injectedKey{}).(map[string]string)
	return h
}
