package usagequery

import "context"

type optionsKey struct{}

type Options struct {
	Identity     string
	Timezone     string
	ForceRefresh bool
}

func WithOptions(ctx context.Context, options Options) context.Context {
	return context.WithValue(ctx, optionsKey{}, options)
}
func OptionsFrom(ctx context.Context) Options {
	options, _ := ctx.Value(optionsKey{}).(Options)
	return options
}
