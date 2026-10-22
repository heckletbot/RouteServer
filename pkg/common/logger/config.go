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

const (
	defaultMaxSize    = 20
	defaultMaxAge     = 30
	defaultMaxBackups = 50
	defaultLevelStr   = "info"
)

// Config is logger config.
type Config struct {
	FileWriterCfg `yaml:",inline"`
	// one of trace, debug, info, warn, error, fatal, panic
	Level string `yaml:"level"`
}

// FileWriterCfg is config of a file writer with rotation.
type FileWriterCfg struct {
	// Enabled indicates whether enable file logger. If it is false, the logger will be
	// written to stdout.
	Enabled bool `yaml:"enabled"`

	// Filename is the file to write logs to.  Backup logger files will be retained
	// in the same directory.  It uses scheduler.logger in os.TempDir() if empty.
	Filename string `yaml:"filename"`

	// MaxSize is the maximum size in megabytes of the logger file before it gets
	// rotated. It defaults to 100 megabytes. It must be positive number,
	// otherwise default number will be used.
	MaxSize int `yaml:"max-size"`

	// MaxAge is the maximum number of days to retain old logger files based on the
	// timestamp encoded in their filename.  Note that a day is defined as 24
	// hours and may not exactly correspond to calendar days due to daylight
	// savings, leap seconds, etc. The default is not to remove old logger files
	// based on age. It must be positive number, otherwise default number will be used.
	MaxAge int `yaml:"max-age"`

	// MaxBackups is the maximum number of old logger files to retain.  The default
	// is to retain all old logger files (though MaxAge may still cause them to get
	// deleted). It must be positive number, otherwise default number will be used.
	MaxBackups int `yaml:"max-backups"`

	// LocalTime determines if the time used for formatting the timestamps in
	// backup files is the computer's local time.  The default is to use UTC
	// time.
	LocalTime bool `yaml:"local-time"`

	// Compress determines if the rotated logger files should be compressed
	// using gzip. The default is not to perform compression.
	Compress bool `yaml:"compress"`

	WriteFormat string `yaml:"write-format"`
}

func (c *Config) ApplyDefault() {
	if c.Level == "" {
		c.Level = defaultLevelStr
	}
}

func NewDefaultFileWriterCfg(fileName string) FileWriterCfg {
	return FileWriterCfg{
		Enabled:    true,
		Filename:   fileName,
		MaxSize:    defaultMaxSize,
		MaxAge:     defaultMaxAge,
		MaxBackups: defaultMaxBackups,
		LocalTime:  true,
		Compress:   true,
	}
}
