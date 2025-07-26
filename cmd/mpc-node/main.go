package main

import (
	"mpc-node/api"
)

func main() {
	router := api.SetupRouter()
	router.Run(":8080") // listen and serve on 0.0.0.0:8080
}
