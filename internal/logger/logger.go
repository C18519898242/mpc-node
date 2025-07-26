package logger

import (
	"mpc-node/internal/config"
	"os"

	"github.com/sirupsen/logrus"
)

// Log is the global logger instance.
var Log = logrus.New()

// InitLogger initializes the global logger based on the provided configuration.
func InitLogger(cfg config.LoggerConfig) error {
	// Set log level
	level, err := logrus.ParseLevel(cfg.Level)
	if err != nil {
		return err
	}
	Log.SetLevel(level)

	// Set log format
	switch cfg.Format {
	case "json":
		Log.SetFormatter(&logrus.JSONFormatter{})
	default:
		Log.SetFormatter(&logrus.TextFormatter{
			FullTimestamp: true,
		})
	}

	// Set output
	Log.SetOutput(os.Stdout)

	return nil
}
