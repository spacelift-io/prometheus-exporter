package logging

import (
	"context"

	"go.uber.org/zap"
)

type loggerKeyType int

const loggerKey loggerKeyType = iota

var defaultLogger *zap.Logger

// Init initializes the logging framework, and returns a new context with a logger attached.
func Init(ctx context.Context, isDevelopment bool) context.Context {
	if isDevelopment {
		defaultLogger, _ = zap.NewDevelopment()
	} else {
		defaultLogger, _ = zap.NewProduction()
	}

	return context.WithValue(ctx, loggerKey, FromContext(ctx))
}

// FromContext returns the logger attached to the context, or the default logger if none is attached.
func FromContext(ctx context.Context) *zap.Logger {
	if ctx == nil {
		return defaultLogger
	}

	l, ok := ctx.Value(loggerKey).(*zap.Logger)
	if ok {
		return l
	}

	return defaultLogger
}
