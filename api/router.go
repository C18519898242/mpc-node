package api

import (
	"mpc-node/api/handlers"
	"net/http"

	"github.com/gin-gonic/gin"
)

func SetupRouter() *gin.Engine {
	router := gin.Default()

	router.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})

	// Key management endpoints
	keyGroup := router.Group("/key")
	{
		keyGroup.POST("/generate", handlers.GenerateKey)
		keyGroup.GET("/list", handlers.ListKeys)
		keyGroup.GET("/:key_id", handlers.GetKeyByKeyID)
	}

	return router
}
