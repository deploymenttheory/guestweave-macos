// File logger backing the logs command (port of lume's daemon log files).
// Lines are appended to weave.info.log / weave.error.log under
// $WEAVE_HOME/logs (so WEAVE_HOME relocates the logs too), with a size-capped
// rotation governed by the `logging` settings block (config.LogMaxSizeBytes /
// LogKeepRotated): once a file exceeds the cap it is renamed to .old (keeping
// one generation) or truncated in place. Logging failures are silent — logging
// must never break a command.
//go:build darwin

package logging

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
)

const (
	LogFileInfoName  = "weave.info.log"
	LogFileErrorName = "weave.error.log"
)

var fileLoggerMutex sync.Mutex

// otelSink, when set, receives every log message in addition to the file
// writer. It is called with the severity ("INFO" or "ERROR") and the already-
// formatted message. Set it via SetOTelSink from the telemetry package at
// process startup.
var otelSink func(severity, message string)

// SetOTelSink registers a function that dual-emits log records to OTel.
// It must be called before any LogInfo/LogError calls to take effect.
// Calling it more than once replaces the previous sink.
func SetOTelSink(fn func(severity, message string)) {
	fileLoggerMutex.Lock()
	defer fileLoggerMutex.Unlock()
	otelSink = fn
}

// LogsDir returns the log directory, creating it on first use. An empty
// string is returned when the home directory cannot be resolved.
func LogsDir() string {
	config, err := weaveconfig.NewConfig()
	if err != nil {
		return ""
	}
	dir := filepath.Join(config.WeaveHomeDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return dir
}

func LogInfo(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	appendLogLine(LogFileInfoName, msg)
	if fn := otelSink; fn != nil {
		fn("INFO", msg)
	}
}

func LogError(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	appendLogLine(LogFileErrorName, msg)
	if fn := otelSink; fn != nil {
		fn("ERROR", msg)
	}
}

// Clear removes every log file — info, error, and their rotated .old copies.
// Used by `weave logs clear` and the run window's Clear Logs menu item.
func Clear() error {
	dir := LogsDir()
	if dir == "" {
		return errors.New("cannot determine the logs directory")
	}

	fileLoggerMutex.Lock()
	defer fileLoggerMutex.Unlock()

	var firstErr error
	for _, name := range []string{
		LogFileInfoName, LogFileErrorName,
		LogFileInfoName + ".old", LogFileErrorName + ".old",
	} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func appendLogLine(fileName string, message string) {
	dir := LogsDir()
	if dir == "" {
		return
	}

	fileLoggerMutex.Lock()
	defer fileLoggerMutex.Unlock()

	path := filepath.Join(dir, fileName)
	if maxSize := weaveconfig.LogMaxSizeBytes(); maxSize > 0 {
		if info, err := os.Stat(path); err == nil && info.Size() >= maxSize {
			if weaveconfig.LogKeepRotated() {
				_ = os.Rename(path, path+".old")
			} else {
				_ = os.Remove(path)
			}
		}
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	_, _ = fmt.Fprintf(file, "%s [%d] %s\n", timestamp, os.Getpid(), message)
}
