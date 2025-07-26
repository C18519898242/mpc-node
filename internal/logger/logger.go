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

	Log.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	// Set output
	if cfg.Path != "" {
		lumberjackLogger := &lumberjack.Logger{
			Filename:   cfg.Path,
			MaxSize:    10, // megabytes
			MaxBackups: 3,
			MaxAge:     28, //days
			Compress:   true,
		}
		// Set output to both file and stdout
		mw := io.MultiWriter(os.Stdout, lumberjackLogger)
		Log.SetOutput(mw)
	} else {
		Log.SetOutput(os.Stdout)
	}

	return nil
}
