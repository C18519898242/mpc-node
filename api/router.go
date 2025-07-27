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
