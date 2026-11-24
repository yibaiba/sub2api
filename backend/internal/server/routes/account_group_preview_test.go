//go:build unit

package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccountGroupPreviewRejectsUsageViewer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	auth := middleware.AdminAuthMiddleware(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUserRole), service.RoleUsageViewer)
		c.Next()
	})
	next := func(c *gin.Context) { c.Next() }
	RegisterAdminRoutes(
		router.Group("/api/v1"), &handler.Handlers{Admin: &handler.AdminHandlers{}},
		auth, middleware.AuditLogMiddleware(next), middleware.StepUpAuthMiddleware(next), nil, nil,
	)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost,
		"/api/v1/admin/accounts/upstream-billing-probe/group-preview", nil))
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "Usage viewer access is limited to account usage")
}
