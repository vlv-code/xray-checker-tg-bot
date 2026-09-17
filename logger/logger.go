package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync/atomic"
)

type Level int

const (
	LevelNone Level = iota
	LevelError
	LevelWarn
	LevelInfo
	LevelDebug
)

var (
	currentLevel atomic.Int32
	errorLogger  = log.New(os.Stderr, "", log.LstdFlags)
	stdLogger    = log.New(os.Stdout, "", log.LstdFlags)
)

func init() {
	currentLevel.Store(int32(LevelInfo))
}

func ParseLevel(s string) Level {
	switch strings.ToLower(s) {
	case "none", "off", "silent":
		return LevelNone
	case "error", "err":
		return LevelError
	case "warn", "warning":
		return LevelWarn
	case "info":
		return LevelInfo
	case "debug":
		return LevelDebug
	default:
		return LevelInfo
	}
}

func (l Level) String() string {
	switch l {
	case LevelNone:
		return "none"
	case LevelError:
		return "error"
	case LevelWarn:
		return "warn"
	case LevelInfo:
		return "info"
	case LevelDebug:
		return "debug"
	default:
		return "unknown"
	}
}

func SetLevel(l Level) {
	currentLevel.Store(int32(l))
	if l == LevelNone {
		stdLogger.SetOutput(io.Discard)
		errorLogger.SetOutput(io.Discard)
	} else {
		stdLogger.SetOutput(os.Stdout)
		errorLogger.SetOutput(os.Stderr)
	}
}

func CurrentLevel() Level {
	return Level(currentLevel.Load())
}

func Debug(format string, v ...any) {
	if Level(currentLevel.Load()) >= LevelDebug {
		stdLogger.Printf("[DEBUG] "+format, v...)
	}
}

func Info(format string, v ...any) {
	if Level(currentLevel.Load()) >= LevelInfo {
		stdLogger.Printf(format, v...)
	}
}

func Warn(format string, v ...any) {
	if Level(currentLevel.Load()) >= LevelWarn {
		stdLogger.Printf("[WARN] "+format, v...)
	}
}

func Error(format string, v ...any) {
	if Level(currentLevel.Load()) >= LevelError {
		errorLogger.Printf("[ERROR] "+format, v...)
	}
}

func Fatal(format string, v ...any) {
	log.Fatalf("[FATAL] "+format, v...)
}

func Startup(format string, v ...any) {
	fmt.Printf(format+"\n", v...)
}

func Result(format string, v ...any) {
	if Level(currentLevel.Load()) >= LevelInfo {
		stdLogger.Printf(format, v...)
	}
}
