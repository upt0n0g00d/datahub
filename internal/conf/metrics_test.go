// Copyright 2021 MIMIRO AS
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package conf

import (
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
	"go.uber.org/zap"

	"github.com/mimiro-io/datahub/internal"
)

var _ = Describe("Start an instance", Ordered, func() {
	var lc fx.Lifecycle
	BeforeAll(func() {
		lc = fxtest.NewLifecycle(internal.FxTestLog(GinkgoT(), false))
	})
	It("should be of noop type when no agent host", func() {
		env := &Env{
			AgentHost: "",
		}
		client, err := NewMetricsClient(lc, env, zap.NewNop().Sugar())
		Expect(err).To(BeNil())
		Expect(reflect.ValueOf(client).Type().String()).To(Equal("*statsd.NoOpClient"))
	})

	It("should be statsd client when agent host set", func() {
		env := &Env{
			AgentHost: "127.0.0.1:8125",
		}
		client, err := NewMetricsClient(lc, env, zap.NewNop().Sugar())
		Expect(err).To(BeNil())
		Expect(reflect.ValueOf(client).Type().String()).To(Equal("*statsd.Client"))
	})
})
