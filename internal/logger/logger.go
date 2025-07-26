package logger

import (
	"io"
	"mpc-node/internal/config"
	"os"
	"path/filepath"

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
	if cfg.FilePath != "" {
		// Create the log directory if it doesn't exist
		logDir := filepath.Dir(cfg.FilePath)
		if err := os.MkdirAll(logDir, os.ModePerm); err != nil {
			return err
		}

		// Open the log file
		file, err := os.OpenFile(cfg.FilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return err
		}

		// Set output to both file and stdout
		mw := io.MultiWriter(os.Stdout, file)
		Log.SetOutput(mw)
	} else {
		Log.SetOutput(os.Stdout)
	}

	return nil
}
