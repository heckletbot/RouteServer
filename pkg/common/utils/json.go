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

package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"infer-dp-scheduler/pkg/common/constant"
)

type JSONModifier struct {
	data map[string]interface{}
}

var SonicApi = sonic.Config{
	UseNumber:   true,
	SortMapKeys: true,
}.Froze()

func NewJSONModifier(jsonStr []byte) (*JSONModifier, error) {
	var data map[string]interface{}
	if err := SonicApi.Unmarshal(jsonStr, &data); err != nil {
		return nil, err
	}
	return &JSONModifier{data: data}, nil
}

func (j *JSONModifier) ShadowCopy() *JSONModifier {
	copied := make(map[string]interface{})
	for k, v := range j.data {
		copied[k] = v
	}
	return &JSONModifier{
		data: copied,
	}
}

func (j *JSONModifier) GetData() (map[string]interface{}, bool) {
	if j.data != nil {
		return j.data, true
	}
	return nil, false
}

func (j *JSONModifier) GetString(key string) (string, bool) {
	if val, exists := j.data[key]; exists {
		if str, ok := val.(string); ok {
			return str, true
		}
	}
	return "", false
}

func (j *JSONModifier) GetNumber(key string) (float64, bool) {
	if val, exists := j.data[key]; exists {
		if num, ok := val.(float64); ok {
			return num, true
		}
		if num, ok := val.(json.Number); ok {
			if res, err := num.Float64(); err == nil {
				return res, true
			}
		}
		if str, ok := val.(string); ok {
			if num, err := strconv.ParseFloat(str, 64); err == nil {
				return num, true
			}
		}
	}
	return 0, false
}

func (j *JSONModifier) GetInt(key string) (int, bool) {
	if val, exists := j.data[key]; exists {
		if i, valid := TryParseInt(val); valid {
			return i, true
		}
	}
	return 0, false
}

func (j *JSONModifier) IsInt(key string) bool {
	val, exists := j.data[key]
	if !exists {
		// if val is not exist, skip param check
		return true
	}
	if num, ok := val.(float64); ok {
		return num == float64(int(num))
	}
	if _, ok := val.(int); ok {
		return true
	}
	if num, ok := val.(json.Number); ok {
		if _, err := num.Int64(); err == nil {
			return true
		}
		if res, err := num.Float64(); err == nil {
			return res == float64(int(res))
		}
	}
	if str, ok := val.(string); ok {
		if _, err := strconv.ParseInt(str, 10, 64); err == nil {
			return true
		}
		if f, err := strconv.ParseFloat(str, 64); err == nil {
			return f == float64(int64(f))
		}
	}
	return false
}

func (j *JSONModifier) GetBool(key string) (bool, bool) {
	if val, exists := j.data[key]; exists {
		return TryParseBool(val)
	}
	return false, false
}

func (j *JSONModifier) IsBool(key string) bool {
	if val, exists := j.data[key]; exists {
		_, valid := TryParseBool(val)
		return valid
	}
	return true
}

func TryParseBool(val interface{}) (bool, bool) {
	if b, ok := val.(bool); ok {
		return b, true
	}
	if num, ok := TryParseInt(val); ok {
		if num == 1 {
			return true, true
		}
		if num == 0 {
			return false, true
		}
	}
	if str, ok := val.(string); ok {
		str = strings.ToLower(str)
		switch str {
		case "on", "y", "yes":
			return true, true
		case "off", "n", "no":
			return false, true
		default:
			if b, err := strconv.ParseBool(str); err == nil {
				return b, true
			}
		}

	}
	return false, false
}

func (j *JSONModifier) GetObject(key string) (map[string]interface{}, bool) {
	if val, exists := j.data[key]; exists {
		if obj, ok := val.(map[string]interface{}); ok {
			return obj, true
		}
	}
	return nil, false
}

func (j *JSONModifier) GetArray(key string) ([]interface{}, bool) {
	if val, exists := j.data[key]; exists {
		if arr, ok := val.([]interface{}); ok {
			return arr, true
		}
	}
	return nil, false
}

func (j *JSONModifier) GetRaw(key string) (interface{}, bool) {
	val, exists := j.data[key]
	return val, exists
}

func (j *JSONModifier) GetImageCount() (int, int) {
	messages, ok := j.data["messages"]
	if !ok {
		return 0, 0 // messages does not exist
	}

	listObj, ok := messages.([]interface{})
	if !ok || len(listObj) == 0 {
		return 0, 0 // type mismatch or empty array
	}
	imageCount := 0
	textStringLen := 0

	for _, item := range listObj {
		message, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		contentObj, ok := message["content"]
		if !ok {
			continue
		}

		contentList, ok := contentObj.([]interface{})
		if !ok {
			if contentStr, ok := contentObj.(string); ok {
				textStringLen += len(contentStr)
			}
			continue
		}

		for _, item := range contentList {
			obj, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if isTextType(obj) {
				if text, ok := obj[constant.TextType].(string); ok {
					textStringLen += len(text)
				}
				continue
			}
			imageCount++
		}
	}

	return imageCount, textStringLen
}

func isTextType(obj map[string]interface{}) bool {
	if typeVal, ok := obj["type"]; ok {
		if typeStr, ok := typeVal.(string); ok {
			return typeStr == constant.TextType
		}
	}
	return false
}

func ReparseArray(dataArr []interface{}, v any) error {
	// Get reflection info of the target value
	targetValue := reflect.ValueOf(v)
	if targetValue.Kind() != reflect.Ptr || targetValue.IsNil() {
		return fmt.Errorf("target must be a non-nil pointer")
	}

	targetElem := targetValue.Elem()
	if targetElem.Kind() != reflect.Slice {
		return fmt.Errorf("target must point to a slice")
	}

	// Get target slice element type
	elemType := targetElem.Type().Elem()

	// Create target slice
	resultSlice := reflect.MakeSlice(reflect.SliceOf(elemType), len(dataArr), len(dataArr))

	// Iterate through the source array, converting elements one by one
	for i, item := range dataArr {
		// Get reflection value of the source element
		sourceValue := reflect.ValueOf(item)

		// If the source element is already the target type or convertible, convert directly
		if sourceValue.Type().ConvertibleTo(elemType) {
			resultSlice.Index(i).Set(sourceValue.Convert(elemType))
		} else {
			// For complex types, fall back to serialization
			itemBytes, err := SonicApi.Marshal(item)
			if err != nil {
				return err
			}

			targetElemValue := reflect.New(elemType).Elem()
			if err := SonicApi.Unmarshal(itemBytes, targetElemValue.Addr().Interface()); err != nil {
				return err
			}

			resultSlice.Index(i).Set(targetElemValue)
		}
	}

	// Set the result to the target value
	targetElem.Set(resultSlice)
	return nil
}

func (j *JSONModifier) Set(key string, value interface{}) {
	j.data[key] = value
}

func (j *JSONModifier) Exists(key string) bool {
	_, ok := j.data[key]
	return ok
}

func (j *JSONModifier) Remove(key string) {
	delete(j.data, key)
}

func (j *JSONModifier) ToJSON() ([]byte, error) {
	bytes, err := SonicApi.Marshal(j.data)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

func (j *JSONModifier) ToIndentedJSON() (string, error) {
	bytes, err := SonicApi.MarshalIndent(j.data, "", "  ")
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func (j *JSONModifier) ToReader() (io.Reader, error) {
	if val, err := j.ToJSON(); err != nil {
		return nil, err
	} else {
		return io.NopCloser(bytes.NewBuffer(val)), nil
	}
}

func (j *JSONModifier) ToReaderWithLength() (io.Reader, int, error) {
	if val, err := j.ToJSON(); err != nil {
		return nil, 0, err
	} else {
		return io.NopCloser(bytes.NewBuffer(val)), len(val), nil
	}
}

func TryParseInt(val interface{}) (int, bool) {
	if num, ok := val.(float64); ok {
		return int(num), true
	}
	if num, ok := val.(int); ok {
		return num, true
	}
	if num, ok := val.(json.Number); ok {
		if res, err := num.Int64(); err == nil {
			return int(res), true
		}
		if res, err := num.Float64(); err == nil {
			return int(res), true
		}
	}
	if str, ok := val.(string); ok {
		if num, err := strconv.ParseInt(str, 10, 64); err == nil {
			return int(num), true
		}
	}
	return 0, false
}
