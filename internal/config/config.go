package config

import (
	"encoding/json"
	"os"
)

// DBConfig holds the database connection parameters.
type DBConfig struct {
	Type     string `json:"type"`
	Host     string `json:"host"`
	User     string `json:"user"`
	Password string `json:"password"`
	DBName   string `json:"dbname"`
	Port     int    `json:"port"`
	SSLMode  string `json:"sslmode"`
	TimeZone string `json:"timezone"`
}

// LoggerConfig holds the logging configuration.
type LoggerConfig struct {
	Level string `json:"level"` // e.g., "debug", "info", "warn", "error"
	Path  string `json:"path"`  // e.g., "logs/mpc-node.log"
}

// Config holds the application's configuration values.
type Config struct {
	ServerPort string       `json:"server_port"`
	NodePorts  []string     `json:"node_ports"`
	Database   DBConfig     `json:"database"`
	Logger     LoggerConfig `json:"logger"`
}

// LoadConfig reads the configuration from a file and returns a Config struct.
func LoadConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	config := &Config{}
	err = decoder.Decode(config)
	if err != nil {
		return nil, err
	}

	return config, nil
}
