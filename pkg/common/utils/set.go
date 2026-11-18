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

package utils

func HasKeyDifference[K comparable, V1, V2 any](map1 map[K]V1, map2 map[K]V2) bool {
	if len(map1) != len(map2) {
		return true
	}

	if len(map1) <= len(map2) {
		for k := range map1 {
			if _, exists := map2[k]; !exists {
				return true
			}
		}
	} else {
		for k := range map2 {
			if _, exists := map1[k]; !exists {
				return true
			}
		}
	}

	return false
}
