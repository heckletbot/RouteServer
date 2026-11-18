/*
 * Copyright 2026 The RouteServer Authors.
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
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRotatingWriter_Rotation(t *testing.T) {
	// Create a temporary directory for test files
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.log")

	// Create config with small file size to trigger rotation quickly
	cfg := &FileWriterCfg{
		Filename:   testFile,
		MaxSize:    1,
		MaxBackups: 3,
	}

	// Override MaxSize for testing by directly setting
	writer, err := NewRotatingWriter(cfg)
	if err != nil {
		t.Fatalf("Failed to create rotating writer: %v", err)
	}

	// Write first message
	msg1 := make([]byte, 1024*1024) // 1MB = 1024*1024 bytes
	for i := range msg1 {
		msg1[i] = 'A'
	}
	msg1 = append(msg1, '\n')
	n, err := writer.Write(msg1)
	if err != nil {
		t.Fatalf("Failed to write first message: %v", err)
	}
	if n != len(msg1) {
		t.Errorf("Expected to write %d bytes, wrote %d", len(msg1), n)
	}

	// Write second message
	msg2 := make([]byte, 1024*1024) // 1MB = 1024*1024 bytes
	for i := range msg2 {
		msg2[i] = 'B'
	}
	msg2 = append(msg2, '\n')
	n, err = writer.Write(msg2)
	if err != nil {
		t.Fatalf("Failed to write second message: %v", err)
	}

	// Check that backup files were created
	files, err := filepath.Glob(filepath.Join(tempDir, "test-*.log*"))
	if err != nil {
		t.Fatalf("Failed to glob backup files: %v", err)
	}

	// Should have at least one backup file
	if len(files) < 1 {
		t.Error("Expected at least one backup file to be created")
	}

	// Close writer
	writer.Close()
}

func TestRotatingWriter_MaxBackups(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "backup_test.log")

	cfg := &FileWriterCfg{
		Filename:   testFile,
		MaxSize:    1,
		MaxBackups: 2,
	}

	writer, err := NewRotatingWriter(cfg)
	if err != nil {
		t.Fatalf("Failed to create rotating writer: %v", err)
	}

	msg := make([]byte, 1024*1024+1) // 1MB = 1024*1024 bytes
	for i := range msg {
		msg[i] = 'A'
	}

	// Write multiple messages to create multiple backups
	for i := 0; i < 5; i++ {

		n, err := writer.Write(msg)
		if err != nil {
			t.Fatalf("Failed to write first message: %v", err)
		}
		if n != len(msg) {
			t.Errorf("Expected to write %d bytes, wrote %d", len(msg), n)
		}
		time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	}

	writer.Close()

	// Count backup files
	files, err := filepath.Glob(filepath.Join(tempDir, "backup_test-*.log*"))
	if err != nil {
		t.Fatalf("Failed to glob backup files: %v", err)
	}

	// Should have at most MaxBackups backup files
	if len(files) > 2 {
		t.Errorf("Expected at most 2 backup files, got %d", len(files))
	}
}

func TestRotatingWriter_WriteFormat(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "format_test.log")

	cfg := &FileWriterCfg{
		Filename: testFile,
		MaxSize:  10, // Large enough for test
	}

	writer, err := NewRotatingWriter(cfg)
	if err != nil {
		t.Fatalf("Failed to create rotating writer: %v", err)
	}
	defer writer.Close()

	// Test writing data
	testData := []byte("Test log message with timestamp\n")
	n, err := writer.Write(testData)
	if err != nil {
		t.Fatalf("Failed to write: %v", err)
	}
	if n != len(testData) {
		t.Errorf("Expected to write %d bytes, wrote %d", len(testData), n)
	}

	// Verify file was created
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Error("Log file was not created")
	}
}

func TestRotatingWriter_ThreadSafety(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "concurrent_test.log")

	cfg := &FileWriterCfg{
		Filename: testFile,
		MaxSize:  10,
	}

	writer, err := NewRotatingWriter(cfg)
	if err != nil {
		t.Fatalf("Failed to create rotating writer: %v", err)
	}
	defer writer.Close()

	// Write from multiple goroutines
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				writer.Write([]byte(fmt.Sprintf("Goroutine %d, Message %d\n", id, j)))
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify file exists and has content
	info, err := os.Stat(testFile)
	if err != nil {
		t.Fatalf("Failed to stat file: %v", err)
	}
	if info.Size() == 0 {
		t.Error("File is empty")
	}
}

func BenchmarkRotatingWriter_Write(b *testing.B) {
	tempDir := b.TempDir()
	testFile := filepath.Join(tempDir, "benchmark.log")

	cfg := &FileWriterCfg{
		Filename: testFile,
		MaxSize:  100,
	}

	writer, err := NewRotatingWriter(cfg)
	if err != nil {
		b.Fatalf("Failed to create rotating writer: %v", err)
	}
	defer writer.Close()

	b.SetParallelism(10240)

	msg := make([]byte, 128) // 128 bytes msg
	for i := range msg {
		msg[i] = 'A'
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			writer.Write(msg)
		}
	})
}
