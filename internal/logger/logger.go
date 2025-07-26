package logger

import (
	"io"
	"mpc-node/internal/config"
	"os"

	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
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
		lumberjackLogger := &lumberjack.Logger{
			Filename:   cfg.FilePath,
			MaxSize:    cfg.MaxSize,
			MaxBackups: cfg.MaxBackups,
			MaxAge:     cfg.MaxAge,
			Compress:   cfg.Compress,
		}
		// Set output to both file and stdout
		mw := io.MultiWriter(os.Stdout, lumberjackLogger)
		Log.SetOutput(mw)
	} else {
		Log.SetOutput(os.Stdout)
	}

	return nil
}
