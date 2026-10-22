/*
 * Copyright 2026 The vLLM-Cloud Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
)

const (
	defaultLevel          = zerolog.InfoLevel
	defaultSkipFrameCount = 5
	defaultLogFileName    = "scheduler.logger"
	defaultTimeFormat     = "2006-01-02 15:04:05.000 -0700"

	JsonFormat = "json"
	TextFormat = "text"
)

var (
	log   *Logger
	lgCtx zerolog.Context
)

func init() {
	lgCtx = zerolog.New(os.Stdout).Level(defaultLevel).With().Timestamp()
	zerolog.TimeFieldFormat = defaultTimeFormat
	zerolog.CallerMarshalFunc = func(pc uintptr, file string, line int) string {
		return filepath.Base(file) + ":" + strconv.Itoa(line)
	}
	lg := lgCtx.CallerWithSkipFrameCount(defaultSkipFrameCount).Logger()
	log = &Logger{
		lg:  &lg,
		cfg: &Config{},
	}
}

type Logger struct {
	lg  *zerolog.Logger
	cfg *Config
}

func Init(cfg Config, rsName string) error {
	zerolog.CallerMarshalFunc = func(pc uintptr, file string, line int) string {
		return rsName + ":" + filepath.Base(file) + ":" + strconv.Itoa(line)
	}
	level, err := zerolog.ParseLevel(strings.ToLower(cfg.Level))
	if err != nil {
		return fmt.Errorf("fail to parse logger level %s: %v", cfg.Level, err)
	}
	lg := log.lg.Level(level).Output(writer(&cfg.FileWriterCfg))
	log.lg = &lg
	return nil
}

func Log() *Logger {
	return log
}

func With() zerolog.Context {
	return lgCtx.Logger().With()
}

// Trace prints Trace level message
func (l *Logger) Trace(msg string) {
	l.writeMsg(l.lg.Trace(), msg)
}

// Tracef prints formatted Trace level message
func (l *Logger) Tracef(format string, args ...interface{}) {
	l.writeMsgf(l.lg.Trace(), format, args...)
}

// Debug prints Debug level message
func (l *Logger) Debug(msg string) {
	l.writeMsg(l.lg.Debug(), msg)
}

// Debugf prints formatted Debug level message
func (l *Logger) Debugf(format string, args ...interface{}) {
	l.writeMsgf(l.lg.Debug(), format, args...)
}

// Info prints Info level message
func (l *Logger) Info(msg string) {
	l.writeMsg(l.lg.Info(), msg)
}

// Infof prints formatted Info level message
func (l *Logger) Infof(format string, args ...interface{}) {
	l.writeMsgf(l.lg.Info(), format, args...)
}

// Warn prints Warn level message
func (l *Logger) Warn(msg string) {
	l.writeMsg(l.lg.Warn(), msg)
}

// Warnf prints formatted Warn level message
func (l *Logger) Warnf(format string, args ...interface{}) {
	l.writeMsgf(l.lg.Warn(), format, args...)
}

// Error prints Error level message
func (l *Logger) Error(msg string) {
	l.writeMsg(l.lg.Error(), msg)
}

// Errorf prints formatted Error level message
func (l *Logger) Errorf(format string, args ...interface{}) {
	l.writeMsgf(l.lg.Error(), format, args...)
}

// Fatal prints Fatal level message
func (l *Logger) Fatal(msg string) {
	l.writeMsg(l.lg.Fatal(), msg)
}

// Fatalf prints formatted Fatal level message
func (l *Logger) Fatalf(format string, args ...interface{}) {
	l.writeMsgf(l.lg.Fatal(), format, args...)
}

// Panic prints panic message with logger.
func (l *Logger) Panic(msg string) {
	l.writeMsg(l.lg.Panic(), msg)
}

// Panicf prints formatted panic message with logger.
func (l *Logger) Panicf(format string, args ...interface{}) {
	l.writeMsgf(l.lg.Panic(), format, args...)
}

func (l *Logger) writeMsg(event *zerolog.Event, msg string) {
	event.Msg(msg)
}

func (l *Logger) writeMsgf(event *zerolog.Event, format string, args ...interface{}) {
	if event == nil {
		return
	}
	l.writeMsg(event, fmt.Sprintf(format, args...))
}

func (l *Logger) writeMsgWithSkip(event *zerolog.Event, extraSkip int, msg string) {
	if event == nil {
		return
	}
	event.CallerSkipFrame(extraSkip)
	l.writeMsg(event, msg)
}

func (l *Logger) writeMsgfWithSkip(event *zerolog.Event, extraSkip int, format string, args ...interface{}) {
	if event == nil {
		return
	}
	event.CallerSkipFrame(extraSkip)
	l.writeMsg(event, fmt.Sprintf(format, args...))
}

func (l *Logger) InfoSkipf(extraSkip int, format string, args ...interface{}) {
	l.writeMsgfWithSkip(l.lg.Info(), extraSkip, format, args...)
}

func (l *Logger) InfoSkip(extraSkip int, msg string) {
	l.writeMsgWithSkip(l.lg.Info(), extraSkip, msg)
}

func (l *Logger) WarnSkipf(extraSkip int, format string, args ...interface{}) {
	l.writeMsgfWithSkip(l.lg.Warn(), extraSkip, format, args...)
}

func (l *Logger) ErrorSkipf(extraSkip int, format string, args ...interface{}) {
	l.writeMsgfWithSkip(l.lg.Error(), extraSkip, format, args...)
}

func (l *Logger) ErrorSkip(extraSkip int, msg string) {
	l.writeMsgWithSkip(l.lg.Error(), extraSkip, msg)
}

func (l *Logger) DebugSkipf(extraSkip int, format string, args ...interface{}) {
	l.writeMsgfWithSkip(l.lg.Debug(), extraSkip, format, args...)
}

func (l *Logger) TraceSkipf(extraSkip int, format string, args ...interface{}) {
	l.writeMsgfWithSkip(l.lg.Trace(), extraSkip, format, args...)
}

func writer(cfg *FileWriterCfg) io.Writer {
	var wr io.Writer = os.Stdout
	if cfg.Enabled {
		wr = io.MultiWriter(os.Stdout, fileWriter(cfg))
	}
	return &Writer{
		out:       wr,
		writeFunc: getWriteFunc(cfg),
	}
}

var logErr func(string)

func getWriteFunc(cfg *FileWriterCfg) func(in []byte, out io.Writer) (int, error) {
	switch cfg.WriteFormat {
	case TextFormat:
		return TextWrite
	case JsonFormat:
		return JsonWrite
	default:
		return JsonWrite
	}
}

func fileWriter(cfg *FileWriterCfg) io.Writer {
	writer, _ := NewRotatingWriter(cfg)
	return writer
}
