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

package list

import (
	"sort"
	"sync/atomic"
)

const ListSize = 1024 * 16

type StatisticsInfo struct {
	Count   int32
	Average int
	Max     int
	Min     int
	P90     int
	P95     int
	P99     int
}

type StatisticsList struct {
	data     []int
	capacity int32
	total    atomic.Int32
}

func NewStatisticsList() *StatisticsList {
	list := &StatisticsList{
		data:     make([]int, ListSize),
		capacity: ListSize,
	}
	list.total.Store(0)
	return list
}

func (sl *StatisticsList) Append(val int) {
	cur := sl.total.Add(1)
	if cur > sl.capacity {
		return
	}
	sl.data[cur-1] = val
}

func (sl *StatisticsList) Statistics() *StatisticsInfo {
	total := sl.total.Load()
	if total == 0 {
		return &StatisticsInfo{
			Count: 0,
		}
	}
	if total > sl.capacity {
		total = sl.capacity
	}
	sort.Ints(sl.data[:total])
	info := &StatisticsInfo{
		Count: total,
		Max:   sl.data[total-1],
		Min:   sl.data[0],
		P90:   sl.data[total*9/10],
		P95:   sl.data[total*95/100],
		P99:   sl.data[total*99/100],
	}
	sum := 0
	for i := int32(0); i < total; i++ {
		sum += sl.data[i]
	}
	info.Average = sum / int(total)
	return info
}
