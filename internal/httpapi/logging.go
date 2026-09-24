package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
)

func (a *API) recovery() gin.HandlerFunc {
	// Gin's default recovery writes request headers and an uncorrelated
	// stack to stderr. Keep recovery inside observability so both the panic
	// and the completed HTTP request use the enriched request context.
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, recovered any) {
		a.logger.ErrorContext(c.Request.Context(), fmt.Sprintf("http request panicked - %s %s", c.Request.Method, c.Request.URL.Path), "error", recovered, "stack", string(debug.Stack()))
		writeAPIError(c, http.StatusInternalServerError, "internal", "internal_error", "An internal error occurred.", nil)
	})
}
