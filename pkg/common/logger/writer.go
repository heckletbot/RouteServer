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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/rs/zerolog"
)

const (
	maxRemainingLength = 2
	bufMapCap          = 100
	FieldSeparator     = " | "
	InnerSeparator     = ": "
)

var bufPool = newBufPool()
var allowedFields = []string{
	zerolog.TimestampFieldName,
	zerolog.LevelFieldName,
	zerolog.CallerFieldName,
	zerolog.MessageFieldName,
}
var sonicApi = sonic.Config{
	UseNumber:   true,
	SortMapKeys: true,
}.Froze()

// Writer struct is the definition of Writer.
type Writer struct {
	out io.Writer

	writeFunc func(in []byte, out io.Writer) (int, error)
}

// Write writes logger to out
func (w *Writer) Write(in []byte) (int, error) {
	return w.writeFunc(in, w.out)
}

func TextWrite(in []byte, out io.Writer) (int, error) {
	var fieldMap OrderedMap
	if err := sonicApi.Unmarshal(in, &fieldMap); err != nil {
		return 0, fmt.Errorf("failed to decode fields: %v", err)
	}

	buf := bufPool.GetBuf()
	defer bufPool.PutBuf(buf)

	writeFixedFields(buf, fieldMap)
	writeFlexedField(buf, fieldMap)
	buf.WriteByte('\n')
	_, err := buf.WriteTo(out)

	return len(in), err
}

func JsonWrite(in []byte, out io.Writer) (int, error) {
	return out.Write(in)
}

func trimPath(path string) string {
	paths := strings.Split(path, "/")
	var finalPath string
	pathLen := len(paths)
	switch {
	case pathLen >= maxRemainingLength:
		finalPath = strings.Join(paths[pathLen-maxRemainingLength:], "/")
	case pathLen == 1:
		finalPath = paths[0]
	default:
		finalPath = path
	}
	return finalPath
}

func writeFixedFields(buf io.StringWriter, fields OrderedMap) {
	for i, allowedField := range allowedFields {
		field, ok := fields.Values[allowedField]
		if !ok {
			continue
		}
		delete(fields.Values, allowedField)
		if i > 0 {
			writeString(buf, FieldSeparator)
		}

		switch allowedField {
		case zerolog.TimestampFieldName:
			timestamp, err := parseTimestamp(field)
			if err != nil || timestamp == nil {
				writeString(buf, "<nil>")
			} else {
				writeString(buf, timestamp.Format(defaultTimeFormat))
			}
		case zerolog.LevelFieldName:
			writeString(buf, strings.ToUpper(field.(string)))
		case zerolog.CallerFieldName:
			s, ok := field.(string)
			if !ok {
				fmt.Printf("type assert error: string")
				return
			}
			trimedPath := trimPath(s)
			writeString(buf, trimedPath)
		default:
			writeString(buf, field.(string))
		}
	}
}

func writeFlexedField(buf io.StringWriter, fields OrderedMap) {
	for _, filedKey := range fields.Keys {
		if fieldValue, exist := fields.Values[filedKey]; exist {
			writeString(buf, FieldSeparator)
			writeString(buf, filedKey)
			writeString(buf, InnerSeparator)
			writeString(buf, fmt.Sprintf("%v", fieldValue))
		}
	}
}

func parseTimestamp(timeIn interface{}) (*time.Time, error) {
	switch t := timeIn.(type) {
	case string:
		timestamp, err := time.Parse(zerolog.TimeFieldFormat, t)
		if err != nil {
			return nil, err
		}
		return &timestamp, nil
	case json.Number:
		timeInteger, err := t.Int64()
		if err != nil {
			return nil, err
		}
		var sec, nsec int64
		switch zerolog.TimeFieldFormat {
		case zerolog.TimeFormatUnixMs:
			nsec = int64(time.Duration(timeInteger) * time.Millisecond)
			sec = 0
		case zerolog.TimeFormatUnixMicro:
			nsec = int64(time.Duration(timeInteger) * time.Microsecond)
			sec = 0
		default:
			return nil, fmt.Errorf("invalid time field format %v", zerolog.TimeFieldFormat)
		}
		timestamp := time.Unix(sec, nsec)
		return &timestamp, nil
	default:
		return nil, nil
	}
}

func writeString(buf io.StringWriter, msg string) {
	// / nolint
	// bytes.Buffer.WriteString function will not return error but nil only.
	if _, err := buf.WriteString(msg); err != nil {
		fmt.Printf("write string error: %v", err)
	}
}

// buffersPool struct is the definition of buffer pool.
type buffersPool struct {
	pool *sync.Pool
}

// newBufPool is used to create an instance of buffer pool.
func newBufPool() *buffersPool {
	pool := &sync.Pool{
		New: func() interface{} {
			return bytes.NewBuffer(make([]byte, 0, bufMapCap))
		},
	}
	return &buffersPool{pool: pool}
}

// GetBuf is used to get buffer from buffer pool.
func (b *buffersPool) GetBuf() *bytes.Buffer {
	return b.pool.Get().(*bytes.Buffer)
}

// PutBuf is used to add buffer to buffer pool.
func (b *buffersPool) PutBuf(buf *bytes.Buffer) {
	buf.Reset()
	b.pool.Put(buf)
}
