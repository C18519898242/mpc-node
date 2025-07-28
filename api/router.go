package api

import (
	"mpc-node/api/handlers"
	"mpc-node/internal/config"
	"mpc-node/internal/network"
	"mpc-node/internal/session"
	"net/http"

	"github.com/gin-gonic/gin"
)

// AppContextMiddleware adds shared services to the context.
func AppContextMiddleware(cfg *config.Config, nodeName string, sm *session.Manager, transport network.Transport) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("config", cfg)
		c.Set("nodeName", nodeName)
		c.Set("sessionManager", sm)
		c.Set("transport", transport)
		c.Next()
	}
}

func SetupRouter(cfg *config.Config, nodeName string, sm *session.Manager, transport network.Transport) *gin.Engine {
	router := gin.Default()

	// Use the app context middleware for all routes
	router.Use(AppContextMiddleware(cfg, nodeName, sm, transport))

	router.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})

	router.POST("/test", handlers.TestCommunication)

	// Key management endpoints
	keyGroup := router.Group("/key")
	{
		keyGroup.POST("/generate", handlers.GenerateKey)
		keyGroup.POST("/sign", handlers.SignMessage)
		keyGroup.POST("/verify", handlers.VerifySignature)
		keyGroup.GET("/list", handlers.ListKeys)
		keyGroup.GET("/:key_id", handlers.GetKeyByKeyID)
	}

	return router
}
