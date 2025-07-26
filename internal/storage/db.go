package storage

import (
	"fmt"
	"mpc-node/internal/storage/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var DB *gorm.DB

// InitDB initializes the database connection.
// In a real application, the DSN should come from a config file.
func InitDB() {
	var err error
	// TODO: Replace with your actual connection string from a config file
	dsn := "host=39.102.213.42 user=postgres password=P@ssw0rd12345678 dbname=mpc_node port=5432 sslmode=disable TimeZone=Asia/Shanghai"
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		panic(fmt.Sprintf("failed to connect to database: %v", err))
	}

	fmt.Println("Database connection successfully established.")

	// Auto-migrate the schema
	err = DB.AutoMigrate(&models.KeyData{}, &models.KeyShare{})
	if err != nil {
		panic(fmt.Sprintf("failed to auto-migrate database: %v", err))
	}
	fmt.Println("Database schema migrated.")
}
