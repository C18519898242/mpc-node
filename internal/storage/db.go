package storage

import (
	"fmt"
	"mpc-node/internal/config"
	"mpc-node/internal/logger"
	"mpc-node/internal/storage/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var DB *gorm.DB

// InitDB initializes the database connection.
func InitDB(cfg config.DBConfig) {
	dsn := fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%d sslmode=%s TimeZone=%s",
		cfg.Host, cfg.User, cfg.Password, cfg.DBName, cfg.Port, cfg.SSLMode, cfg.TimeZone)
	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		logger.Log.Fatalf("Failed to connect to database: %v", err)
	}

	logger.Log.Info("Database connection successfully established.")

	// Auto-migrate the schema
	err = DB.AutoMigrate(&models.KeyData{}, &models.KeyShare{})
	if err != nil {
		logger.Log.Fatalf("Failed to auto-migrate database: %v", err)
	}
	logger.Log.Info("Database schema migrated.")
}
