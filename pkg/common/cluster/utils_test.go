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

package cluster

import (
	"testing"

	"infer-dp-scheduler/pkg/common/utils"
)

func TestCheckPeerValid(t *testing.T) {
	type args struct {
		peer *utils.Peer
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "invalid name",
			args: args{
				peer: &utils.Peer{
					Name: "./",
					Addr: utils.PeerAddr{
						Host:     "1.2.3.4",
						HttpPort: 1234,
						OpsPort:  1250,
						Domain:   "xxx",
					},
					IsCandidate: false,
				},
			},
			wantErr: true,
		},
		{
			name: "invalid name",
			args: args{
				peer: &utils.Peer{
					Name: "*",
					Addr: utils.PeerAddr{
						Host:     "1.2.3.4",
						HttpPort: 1234,
						OpsPort:  1250,
						Domain:   "xxx",
					},
					IsCandidate: false,
				},
			},
			wantErr: true,
		},
		{
			name: "invalid name",
			args: args{
				peer: &utils.Peer{
					Name: "../",
					Addr: utils.PeerAddr{
						Host:     "1.2.3.4",
						HttpPort: 1234,
						OpsPort:  1250,
						Domain:   "xxx",
					},
					IsCandidate: false,
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckPeerValid(tt.args.peer); (err != nil) != tt.wantErr {
				t.Errorf("CheckPeerValid() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
