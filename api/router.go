package api

import (
	"mpc-node/api/handlers"
	"mpc-node/internal/config"
	"net/http"

	"github.com/gin-gonic/gin"
)

// ConfigMiddleware adds the config to the context
func ConfigMiddleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("config", cfg)
		c.Next()
	}
}

func SetupRouter(cfg *config.Config) *gin.Engine {
	router := gin.Default()

	// Use the config middleware for all routes
	router.Use(ConfigMiddleware(cfg))

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
