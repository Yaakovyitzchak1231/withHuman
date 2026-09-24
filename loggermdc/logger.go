package loggermdc

import "log/slog"

// Configure installs the process logger, including package-level slog calls.
// Call it once in main, before composing services or starting goroutines.
func Configure(handler slog.Handler) *slog.Logger {
	logger := Wrap(slog.New(handler))
	slog.SetDefault(logger)
	return logger
}

// Wrap ensures an injected logger extracts MDC without changing its output,
// level or bound attributes. A nil logger uses the process default.
func Wrap(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	if _, ok := logger.Handler().(*Handler); ok {
		return logger
	}
	return slog.New(NewHandler(logger.Handler()))
}
