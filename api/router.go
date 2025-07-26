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

	// New endpoint to trigger key generation
	router.POST("/keys", handlers.GenerateKey)
	router.GET("/keys", handlers.GetKeys)

	return router
}
