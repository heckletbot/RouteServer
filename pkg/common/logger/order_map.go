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
	"bytes"
	"encoding/json"
	"fmt"
)

type OrderedMap struct {
	Keys   []string
	Values map[string]interface{}
}

func (om *OrderedMap) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // Preserve number precision

	// Check opening delimiter
	if t, err := dec.Token(); err != nil {
		return err
	} else if delim, ok := t.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("expected JSON object")
	}

	om.Values = make(map[string]interface{})
	om.Keys = make([]string, 0, 10) // Pre-allocate initial capacity

	for dec.More() {
		// Parse key
		t, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := t.(string)
		if !ok {
			return fmt.Errorf("failed to unmarshal json, due to non string key is found")
		}

		// Parse value
		var val interface{}
		if err := dec.Decode(&val); err != nil {
			return err
		}

		// Store ordered key-value pair
		om.Keys = append(om.Keys, key)
		om.Values[key] = val
	}

	// Check closing delimiter
	if t, err := dec.Token(); err != nil {
		return err
	} else if delim, ok := t.(json.Delim); !ok || delim != '}' {
		return fmt.Errorf("expected end of object")
	}

	return nil
}
