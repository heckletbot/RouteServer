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
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	// File permissions
	// Directory permissions: owner has read, write, execute permissions only
	DirPerm = 0700
	// Log file permissions: owner has read and write permissions only
	LogFilePerm = 0600
	// Archived log file permissions: owner has read permission only
	ArchiveFilePerm = 0400
)

// RotatingWriter implements io.Writer with file rotation support
type RotatingWriter struct {
	Filepath   string
	MaxSize    int  // Maximum file size in MB
	MaxAge     int  // Maximum age in days
	MaxBackups int  // Maximum number of backup files
	LocalTime  bool // Use local time for timestamps
	GzipEnable bool // Enable gzip compression for old files

	currentFile *os.File
	currentSize int64
	baseName    string
	ext         string
	mutex       sync.Mutex
	asyncError  error
}

func NewRotatingWriter(cfg *FileWriterCfg) (*RotatingWriter, error) {
	if cfg == nil {
		defaultCfg := NewDefaultFileWriterCfg(defaultLogFileName)
		cfg = &defaultCfg
	}
	rw := &RotatingWriter{
		MaxSize:    cfg.MaxSize,
		MaxAge:     cfg.MaxAge,
		MaxBackups: cfg.MaxBackups,
		LocalTime:  cfg.LocalTime,
		GzipEnable: cfg.Compress,
	}

	if len(cfg.Filename) == 0 {
		rw.Filepath = filepath.Join(os.TempDir(), defaultLogFileName)
	} else {
		rw.Filepath = cfg.Filename
	}

	// Extract base name and extension
	rw.baseName, rw.ext = filepath.Base(rw.Filepath), ""
	if ext := filepath.Ext(rw.Filepath); ext != "" {
		rw.baseName = rw.Filepath[:len(rw.Filepath)-len(ext)]
		rw.ext = ext
	}

	// Open initial file
	if err := rw.openNewFile(); err != nil {
		return nil, fmt.Errorf("failed to open initial log file: %w", err)
	}

	return rw, nil
}

func (rw *RotatingWriter) Write(p []byte) (n int, err error) {
	rw.mutex.Lock()
	defer rw.mutex.Unlock()

	if rw.currentFile == nil {
		if err = rw.openNewFile(); err != nil {
			return 0, fmt.Errorf("failed to open log file: %w", err)
		}
	}

	// Check if rotation is needed
	if rw.NeedRotation() {
		if err = rw.rotate(); err != nil {
			return 0, fmt.Errorf("failed to rotate log file: %w", err)
		}
	}

	n, err = rw.currentFile.Write(p)
	if err != nil {
		return n, fmt.Errorf("failed to write to log file: %w", err)
	}

	rw.currentSize += int64(n)

	// Check for any pending async errors and return them first
	if rw.asyncError != nil {
		err := rw.asyncError
		rw.asyncError = nil
		return 0, fmt.Errorf("background rotation error: %w", err)
	}
	return n, nil
}

func (rw *RotatingWriter) NeedRotation() bool {
	if rw.MaxSize <= 0 {
		return false
	}

	// Convert MaxSize from MB to bytes
	maxSizeBytes := int64(rw.MaxSize) * 1024 * 1024
	return rw.currentSize >= maxSizeBytes
}

// file rotation
func (rw *RotatingWriter) rotate() error {
	// Close current file
	if rw.currentFile != nil {
		if err := rw.currentFile.Close(); err != nil {
			rw.currentFile = nil
			return fmt.Errorf("failed to close current log file: %w", err)
		}
		rw.currentFile = nil
	}

	// Generate backup filename
	backupName := rw.generateBackupName()

	// Move current file to back up
	if err := os.Rename(rw.Filepath, backupName); err != nil {
		return fmt.Errorf("failed to rotate backup file: %w", err)
	}

	// Change file permissions to read-only to prevent further writes
	if err := os.Chmod(backupName, ArchiveFilePerm); err != nil {
		return fmt.Errorf("failed to change permissions on backup file: %w", err)
	}

	// Queue file for compression if enabled
	if rw.GzipEnable {
		go func() {
			if err := rw.compressFile(backupName); err != nil {
				rw.mutex.Lock()
				if rw.asyncError == nil {
					rw.asyncError = fmt.Errorf("compression error: %w", err)
				}
				rw.mutex.Unlock()
			}
		}()
	}

	// asynchronously clean up
	go func() {
		if err := rw.cleanOldFiles(); err != nil {
			rw.mutex.Lock()
			if rw.asyncError == nil {
				rw.asyncError = fmt.Errorf("cleanup error: %w", err)
			}
			rw.mutex.Unlock()
		}
	}()

	// Open new file
	if err := rw.openNewFile(); err != nil {
		return fmt.Errorf("failed to open new log file after rotation: %w", err)
	}

	return nil
}

// generateBackupName generates a unique backup filename
func (rw *RotatingWriter) generateBackupName() string {
	t := time.Now()
	if rw.LocalTime {
		t = t.Local()
	} else {
		t = t.UTC()
	}

	timestamp := t.Format("2006-01-02T15-04-05.000")
	return fmt.Sprintf("%s-%s%s", rw.baseName, timestamp, rw.ext)
}

// openNewFile opens a new log file for writing
func (rw *RotatingWriter) openNewFile() error {
	// Ensure directory exists
	dir := filepath.Dir(rw.Filepath)
	if dir != "" {
		if err := os.MkdirAll(dir, DirPerm); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
		}
	}

	file, err := os.OpenFile(rw.Filepath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, LogFilePerm)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	rw.currentFile = file

	// Get current file size
	if info, err := file.Stat(); err == nil {
		rw.currentSize = info.Size()
	} else {
		rw.currentSize = 0
		return fmt.Errorf("failed to stat log file: %w", err)
	}

	return nil
}

// cleanOldFiles removes old log files based on MaxAge and MaxBackups
func (rw *RotatingWriter) cleanOldFiles() error {
	dir := filepath.Dir(rw.Filepath)
	prefix := filepath.Base(rw.baseName)

	// Collect errors during cleanup
	var cleanupErrors []string

	// check both files and compressed files
	files, err := filepath.Glob(filepath.Join(dir, prefix+"-*"+rw.ext))
	if err != nil {
		return fmt.Errorf("failed to glob log files for cleanup: %w", err)
	}
	compressedFiles, err := filepath.Glob(filepath.Join(dir, prefix+"-*"+rw.ext+".gz"))
	if err != nil {
		return fmt.Errorf("failed to glob log files for cleanup: %w", err)
	}
	files = append(files, compressedFiles...)

	var fileInfos []struct {
		name     string
		modTime  time.Time
		fileSize int64
	}

	for _, file := range files {
		if info, err := os.Stat(file); err == nil {
			fileInfos = append(fileInfos, struct {
				name     string
				modTime  time.Time
				fileSize int64
			}{name: file, modTime: info.ModTime(), fileSize: info.Size()})
		} else {
			// Skip inaccessible files but record the error
			cleanupErrors = append(cleanupErrors, fmt.Sprintf("stat failed for %s: %v", file, err))
			continue
		}
	}

	// Sort by modification time
	sort.Slice(fileInfos, func(i, j int) bool {
		return fileInfos[i].modTime.After(fileInfos[j].modTime)
	})

	now := time.Now()
	if rw.LocalTime {
		now = now.Local()
	} else {
		now = now.UTC()
	}

	// Remove files older than MaxAge
	if rw.MaxAge > 0 {
		cutoff := now.AddDate(0, 0, -rw.MaxAge)
		for _, fi := range fileInfos {
			if fi.modTime.Before(cutoff) {
				if err := os.Remove(fi.name); err != nil {
					cleanupErrors = append(cleanupErrors, fmt.Sprintf("remove failed for %s: %v", fi.name, err))
					continue
				}
			}
		}
	}

	// Keep only MaxBackups files
	if rw.MaxBackups > 0 && len(fileInfos) > rw.MaxBackups {
		for i := rw.MaxBackups; i < len(fileInfos); i++ {
			if err := os.Remove(fileInfos[i].name); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Sprintf("remove failed for %s: %v", fileInfos[i].name, err))
			}
		}
	}

	// Return collected errors if any
	if len(cleanupErrors) > 0 {
		return fmt.Errorf("cleanup completed with %d errors: %v", len(cleanupErrors), cleanupErrors)
	}

	return nil
}

// compresses a log file using gzip
func (rw *RotatingWriter) compressFile(filename string) error {

	// Open source file
	src, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("failed to open file for compression %s: %w", filename, err)
	}

	dstName := filename + ".gz"
	dst, err := os.OpenFile(dstName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, LogFilePerm)
	if err != nil {
		if err := src.Close(); err != nil {
			return fmt.Errorf("failed to close file %s while compress failed: %w", filename, err)
		}
		return fmt.Errorf("failed to create compressed file %s: %w", dstName, err)
	}
	defer dst.Close()

	// Create gzip writer
	gw := gzip.NewWriter(dst)
	defer gw.Close()

	// Copy data
	if _, err := io.Copy(gw, src); err != nil {
		if err := src.Close(); err != nil {
			return fmt.Errorf("failed to close file %s while compress failed: %w", filename, err)
		}
		if err := os.Remove(dstName); err != nil {
			return fmt.Errorf("failed to remove file %s while compress failed: %w", dstName, err)
		}
		return fmt.Errorf("failed to compress file %s to %s: %w", filename, dstName, err)
	}

	if err := src.Close(); err != nil {
		return fmt.Errorf("failed to close file %s ： %w", filename, err)
	}
	// Remove original file after successful compression
	if err := os.Remove(filename); err != nil {
		return fmt.Errorf("failed to remove original file after compression %s: %w", filename, err)
	}

	if err := os.Chmod(dstName, ArchiveFilePerm); err != nil {
		return fmt.Errorf("failed to change permissions on backup file: %w", err)
	}
	return nil
}

func (rw *RotatingWriter) Close() error {
	rw.mutex.Lock()
	defer rw.mutex.Unlock()

	if rw.currentFile != nil {
		err := rw.currentFile.Close()
		rw.currentFile = nil
		if err != nil {
			return fmt.Errorf("failed to close log file: %w", err)
		}
	}

	return nil
}
