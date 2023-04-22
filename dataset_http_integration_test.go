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

package datahub_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/franela/goblin"
	"github.com/labstack/echo/v4"
	"go.uber.org/fx"

	"github.com/mimiro-io/datahub"
	"github.com/mimiro-io/datahub/internal/server"
)

func TestHttp(t *testing.T) {
	g := goblin.Goblin(t)

	var app *fx.App
	var mockLayer *MockLayer

	location := "./http_integration_test"
	queryURL := "http://localhost:24997/query"
	dsURL := "http://localhost:24997/datasets/bananas"
	proxyDsURL := "http://localhost:24997/datasets/cucumbers"
	datasetsURL := "http://localhost:24997/datasets"

	g.Describe("The dataset endpoint", func() {
		g.Before(func() {
			_ = os.RemoveAll(location)
			_ = os.Setenv("STORE_LOCATION", location)
			_ = os.Setenv("PROFILE", "test")
			_ = os.Setenv("SERVER_PORT", "24997")
			_ = os.Setenv("FULLSYNC_LEASE_TIMEOUT", "500ms")

			oldOut := os.Stdout
			oldErr := os.Stderr
			devNull, _ := os.Open("/dev/null")
			os.Stdout = devNull
			os.Stderr = devNull
			app, _ = datahub.Start(context.Background())
			os.Stdout = oldOut
			os.Stderr = oldErr
			mockLayer = NewMockLayer()
			go func() {
				_ = mockLayer.echo.Start(":7778")
			}()
		})
		g.After(func() {
			_ = mockLayer.echo.Shutdown(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 1000*time.Millisecond)
			err := app.Stop(ctx)
			defer cancel()
			g.Assert(err).IsNil()
			err = os.RemoveAll(location)
			g.Assert(err).IsNil()
			_ = os.Unsetenv("STORE_LOCATION")
			_ = os.Unsetenv("PROFILE")
			_ = os.Unsetenv("SERVER_PORT")
			_ = os.Unsetenv("FULLSYNC_LEASE_TIMEOUT")
		})

		g.Describe("The dataset root API", func() {
			g.It("Should create a regular dataset", func() {
				// create new dataset
				res, err := http.Post(dsURL, "application/json", strings.NewReader(""))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
			})
			g.It("Should retrieve a regular dataset", func() {
				res, err := http.Get(dsURL)
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				b, _ := io.ReadAll(res.Body)
				m := map[string]interface{}{}
				_ = json.Unmarshal(b, &m)
				g.Assert(m["id"]).Eql("ns0:bananas")
				refs := m["refs"].(map[string]interface{})
				g.Assert(refs["ns2:type"]).Eql("ns1:dataset")
			})
			g.It("Should create a proxy dataset", func() {
				// create new dataset
				/*
					type createDatasetConfig struct {
						ProxyDatasetConfig *proxyDatasetConfig `json:"proxyDatasetConfig"`
						PublicNamespaces   []string            `json:"publicNamespaces"`
					}

					type proxyDatasetConfig struct {
						AuthProvider        string `json:"authProvider"`
						RemoteUrl           string `json:"remoteUrl"`
						UpstreamTransform   string `json:"upstreamTransform"`
						DownstreamTransform string `json:"downstreamTransform"`
					}
				*/
				res, err := http.Post(proxyDsURL+"?proxy=true", "application/json", strings.NewReader(
					`{"proxyDatasetConfig": {"remoteUrl": "http://localhost:7778/datasets/tomatoes"}}`))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
			})
			g.It("Should reject a proxy dataset if misconfigured", func() {
				res, err := http.Post(proxyDsURL+"2?proxy=true", "application/json", strings.NewReader(
					`{"proxyDatasetConfig": {"remoteUrl": ""}}`))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(400)
				b, _ := io.ReadAll(res.Body)
				g.Assert(string(b)).Eql("{\"message\":\"invalid proxy configuration provided\"}\n")
			})
			g.It("Should retrieve a proxy dataset", func() {
				res, err := http.Get(proxyDsURL)
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				b, _ := io.ReadAll(res.Body)
				m := map[string]interface{}{}
				_ = json.Unmarshal(b, &m)
				g.Assert(m["id"]).Eql("ns0:cucumbers")
				refs := m["refs"].(map[string]interface{})
				g.Assert(refs["ns2:type"]).Eql("ns1:proxy-dataset")
			})
			g.It("Should list both regular and proxy datasets", func() {
				res, err := http.Get(datasetsURL)
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				b, _ := io.ReadAll(res.Body)
				var l []map[string]interface{}
				_ = json.Unmarshal(b, &l)
				g.Assert(len(l)).Eql(3, "core.Dataset, bananas, cucumbers are listed")
			})
		})
		g.Describe("The /entities and /changes API endpoints for regular datasets", func() {
			g.It("Should accept a single batch of changes", func() {
				// populate dataset
				payload := strings.NewReader(bananasFromTo(1, 10, false))
				res, err := http.Post(dsURL+"/entities", "application/json", payload)

				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				// read it back
				res, err = http.Get(dsURL + "/changes")
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				bodyBytes, err := io.ReadAll(res.Body)
				g.Assert(err).IsNil()
				var entities []*server.Entity
				err = json.Unmarshal(bodyBytes, &entities)
				g.Assert(err).IsNil()
				g.Assert(len(entities)).Eql(12, "expected 10 entities plus @context and @continuation")
			})

			g.It("Should accept multiple overlapping batches of changes", func() {
				// replace 5-10 and add 11-15
				payload := strings.NewReader(bananasFromTo(5, 15, false))
				res, err := http.Post(dsURL+"/entities", "application/json", payload)
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				// replace 10-15 and add 16-20
				payload = strings.NewReader(bananasFromTo(10, 20, false))
				res, err = http.Post(dsURL+"/entities", "application/json", payload)
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				// read it back
				res, err = http.Get(dsURL + "/changes")
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				bodyBytes, _ := io.ReadAll(res.Body)
				_ = res.Body.Close()
				var entities []*server.Entity
				err = json.Unmarshal(bodyBytes, &entities)
				g.Assert(err).IsNil()
				g.Assert(len(entities)).Eql(22, "expected 20 entities plus @context and @continuation")
			})

			g.It("Should record deleted states", func() {
				payload := strings.NewReader(bananasFromTo(7, 8, true))
				res, err := http.Post(dsURL+"/entities", "application/json", payload)
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				// read changes back
				res, err = http.Get(dsURL + "/changes")
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				bodyBytes, _ := io.ReadAll(res.Body)
				_ = res.Body.Close()
				var entities []*server.Entity
				err = json.Unmarshal(bodyBytes, &entities)
				g.Assert(err).IsNil()
				g.Assert(len(entities)).
					Eql(24, "expected 20 entities plus 2 deleted-changes plus @context and @continuation")
				g.Assert(entities[7].IsDeleted).IsFalse("original change 7 is still undeleted")
				g.Assert(entities[22].IsDeleted).IsTrue("deleted state for 7  is a new change at end of list")

				// read entities back
				res, err = http.Get(dsURL + "/entities")
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				bodyBytes, _ = io.ReadAll(res.Body)
				_ = res.Body.Close()
				entities = nil
				err = json.Unmarshal(bodyBytes, &entities)
				g.Assert(err).IsNil()
				g.Assert(len(entities)).Eql(22, "expected 20 entities plus @context and @continuation")
				g.Assert(entities[7].IsDeleted).IsTrue("entity 7 is deleted")
			})

			g.It("Should do deletion detection in a fullsync", func() {
				// only send IDs 4 through 16 in batches as fullsync
				// 1-3 and 17-20 should end up deleted

				// first batch with "start" header
				payload := strings.NewReader(bananasFromTo(4, 8, false))
				ctx, cancel := context.WithTimeout(context.Background(), 1000*time.Millisecond)
				req, _ := http.NewRequestWithContext(ctx, "POST", dsURL+"/entities", payload)
				req.Header.Add("universal-data-api-full-sync-start", "true")
				req.Header.Add("universal-data-api-full-sync-id", "42")
				_, err := http.DefaultClient.Do(req)
				g.Assert(err).IsNil()
				cancel()

				// 2nd batch
				payload = strings.NewReader(bananasFromTo(9, 12, false))
				ctx, cancel = context.WithTimeout(context.Background(), 1000*time.Millisecond)
				req, _ = http.NewRequestWithContext(ctx, "POST", dsURL+"/entities", payload)
				req.Header.Add("universal-data-api-full-sync-id", "42")
				_, _ = http.DefaultClient.Do(req)
				cancel()

				// last batch with "end" signal
				payload = strings.NewReader(bananasFromTo(13, 16, false))
				ctx, cancel = context.WithTimeout(context.Background(), 1000*time.Millisecond)
				req, _ = http.NewRequestWithContext(ctx, "POST", dsURL+"/entities", payload)
				req.Header.Add("universal-data-api-full-sync-id", "42")
				req.Header.Add("universal-data-api-full-sync-end", "true")
				_, _ = http.DefaultClient.Do(req)
				cancel()

				// read changes back
				res, err := http.Get(dsURL + "/changes")
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				bodyBytes, _ := io.ReadAll(res.Body)
				_ = res.Body.Close()
				var entities []*server.Entity
				err = json.Unmarshal(bodyBytes, &entities)
				g.Assert(err).IsNil()
				g.Assert(len(entities)).Eql(33, "expected 20 entities plus 11 changes and @context and @continuation")
				g.Assert(entities[7].IsDeleted).IsFalse("original change 7 is still undeleted")
				g.Assert(entities[21].IsDeleted).IsTrue("deleted state for 7  is a new change at end of list")

				// read entities back
				res, err = http.Get(dsURL + "/entities")
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				bodyBytes, _ = io.ReadAll(res.Body)
				_ = res.Body.Close()
				entities = nil
				err = json.Unmarshal(bodyBytes, &entities)
				g.Assert(err).IsNil()
				g.Assert(len(entities)).Eql(22, "expected 20 entities plus @context and @continuation")
				// remove context
				entities = entities[1:]
				for i := 0; i < 3; i++ {
					g.Assert(entities[i].IsDeleted).IsTrue("entity was not part of fullsync, should be deleted: ", i)
				}
				for i := 3; i < 16; i++ {
					g.Assert(entities[i].IsDeleted).IsFalse("entity was part of fullsync, should be active: ", i)
				}
				for i := 16; i < 20; i++ {
					g.Assert(entities[i].IsDeleted).IsTrue("entity was not part of fullsync, should be deleted: ", i)
				}
			})

			g.It("should keep fullsync requests with same sync-id in parallel", func() {
				// only send IDs 4 through 16 in batches as fullsync
				// 1-3 and 17-20 should end up deleted

				// first batch with "start" header
				payload := strings.NewReader(bananasFromTo(4, 4, false))
				ctx, cancel := context.WithTimeout(context.Background(), 1000*time.Millisecond)
				req, _ := http.NewRequestWithContext(ctx, "POST", dsURL+"/entities", payload)
				req.Header.Add("universal-data-api-full-sync-start", "true")
				req.Header.Add("universal-data-api-full-sync-id", "43")
				_, _ = http.DefaultClient.Do(req)
				cancel()

				// next, updated id 5 with wrong sync-id. should not be registered as "seen" and therefore be deleted after fs
				payload = strings.NewReader(bananasFromTo(5, 5, false))
				ctx, cancel = context.WithTimeout(context.Background(), 1000*time.Millisecond)
				req, _ = http.NewRequestWithContext(ctx, "POST", dsURL+"/entities", payload)
				req.Header.Add("universal-data-api-full-sync-id", "44")
				res, err := http.DefaultClient.Do(req)
				g.Assert(err).IsNil()
				cancel()
				g.Assert(res.StatusCode).Eql(409, "request should be rejected because fullsync is going on")

				// also try to add id 5 without sync-id. should still be rejected
				payload = strings.NewReader(bananasFromTo(5, 5, false))
				ctx, cancel = context.WithTimeout(context.Background(), 1000*time.Millisecond)
				req, _ = http.NewRequestWithContext(ctx, "POST", dsURL+"/entities", payload)
				res, err = http.DefaultClient.Do(req)
				g.Assert(err).IsNil()
				cancel()
				g.Assert(res.StatusCode).Eql(409, "request should be rejected because fullsync is going on")

				// 10 batches in parallel with correct sync-id
				wg := sync.WaitGroup{}
				for i := 6; i < 16; i++ {
					wg.Add(1)
					id := i
					go func() {
						payloadLocal := strings.NewReader(bananasFromTo(id, id, false))
						ctxLocal, cancelLocal := context.WithTimeout(context.Background(), 1000*time.Millisecond)
						reqLocal, _ := http.NewRequestWithContext(ctxLocal, "POST", dsURL+"/entities", payloadLocal)
						reqLocal.Header.Add("universal-data-api-full-sync-id", "43")
						_, _ = http.DefaultClient.Do(reqLocal)
						cancelLocal()
						wg.Done()
					}()
				}

				wg.Wait()

				// last batch with "end" signal
				payload = strings.NewReader(bananasFromTo(16, 16, false))
				ctx, cancel = context.WithTimeout(context.Background(), 1000*time.Millisecond)
				req, _ = http.NewRequestWithContext(ctx, "POST", dsURL+"/entities", payload)
				req.Header.Add("universal-data-api-full-sync-id", "43")
				req.Header.Add("universal-data-api-full-sync-end", "true")
				_, _ = http.DefaultClient.Do(req)
				cancel()

				// read changes back
				res, err = http.Get(dsURL + "/changes")
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				bodyBytes, _ := io.ReadAll(res.Body)
				_ = res.Body.Close()
				var entities []*server.Entity
				err = json.Unmarshal(bodyBytes, &entities)
				g.Assert(err).IsNil()
				g.Assert(len(entities)).
					Eql(34, "expected 31 changes from before plus deletion of id5 and @context and @continuation")
				g.Assert(entities[32].IsDeleted).IsTrue("deleted state for 5  is a new change at end of list")
			})
			g.It("should abandon fullsync when new fullsync is started", func() {
				// start a fullsync
				payload := strings.NewReader(bananasFromTo(1, 1, false))
				ctx, cancel := context.WithTimeout(context.Background(), 1000*time.Millisecond)
				req, _ := http.NewRequestWithContext(ctx, "POST", dsURL+"/entities", payload)
				req.Header.Add("universal-data-api-full-sync-start", "true")
				req.Header.Add("universal-data-api-full-sync-id", "45")
				res, err := http.DefaultClient.Do(req)
				cancel()
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				// start another fullsync
				payload = strings.NewReader(bananasFromTo(1, 1, false))
				ctx, cancel = context.WithTimeout(context.Background(), 1000*time.Millisecond)
				req, _ = http.NewRequestWithContext(ctx, "POST", dsURL+"/entities", payload)
				req.Header.Add("universal-data-api-full-sync-start", "true")
				req.Header.Add("universal-data-api-full-sync-id", "46")
				res, err = http.DefaultClient.Do(req)
				cancel()
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				// try to append to first fullsync, should be rejected
				payload = strings.NewReader(bananasFromTo(2, 2, false))
				ctx, cancel = context.WithTimeout(context.Background(), 1000*time.Millisecond)
				req, _ = http.NewRequestWithContext(ctx, "POST", dsURL+"/entities", payload)
				req.Header.Add("universal-data-api-full-sync-id", "45")
				res, err = http.DefaultClient.Do(req)
				cancel()
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(409, "expect rejection since syncid 45 is not active anymore")

				// complete second sync
				payload = strings.NewReader(bananasFromTo(16, 16, false))
				ctx, cancel = context.WithTimeout(context.Background(), 1000*time.Millisecond)
				req, _ = http.NewRequestWithContext(ctx, "POST", dsURL+"/entities", payload)
				req.Header.Add("universal-data-api-full-sync-id", "46")
				req.Header.Add("universal-data-api-full-sync-end", "true")
				res, err = http.DefaultClient.Do(req)
				cancel()
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200, "sync 46 accept requests")
			})
			g.It("should abandon fullsync after a timeout period without new requests", func() {
				// start a fullsync
				payload := strings.NewReader(bananasFromTo(1, 1, false))
				ctx, cancel := context.WithTimeout(context.Background(), 1000*time.Millisecond)
				req, _ := http.NewRequestWithContext(ctx, "POST", dsURL+"/entities", payload)
				req.Header.Add("universal-data-api-full-sync-start", "true")
				req.Header.Add("universal-data-api-full-sync-id", "47")
				res, err := http.DefaultClient.Do(req)
				cancel()
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				// exceed fullsync timeout
				time.Sleep(501 * time.Millisecond)

				// send next fullsync batch. should be OK even though lease is timed out
				payload = strings.NewReader(bananasFromTo(2, 2, false))
				ctx, cancel = context.WithTimeout(context.Background(), 1000*time.Millisecond)
				req, _ = http.NewRequestWithContext(ctx, "POST", dsURL+"/entities", payload)
				req.Header.Add("universal-data-api-full-sync-id", "47")
				res, err = http.DefaultClient.Do(req)
				cancel()
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				// send next end signal. should produce error since lease should have timed out
				payload = strings.NewReader(bananasFromTo(3, 3, false))
				ctx, cancel = context.WithTimeout(context.Background(), 1000*time.Millisecond)
				req, _ = http.NewRequestWithContext(ctx, "POST", dsURL+"/entities", payload)
				req.Header.Add("universal-data-api-full-sync-end", "true")
				req.Header.Add("universal-data-api-full-sync-id", "47")
				res, err = http.DefaultClient.Do(req)
				cancel()
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(410)
			})

			g.It("Should pageinate over entities with continuation token", func() {
				payload := strings.NewReader(bananasFromTo(1, 100, false))
				res, err := http.Post(dsURL+"/entities", "application/json", payload)
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				// read first page of 10 entities back
				res, err = http.Get(dsURL + "/entities?limit=10")
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				bodyBytes, _ := io.ReadAll(res.Body)
				_ = res.Body.Close()
				var entities []*server.Entity
				err = json.Unmarshal(bodyBytes, &entities)
				var m []map[string]interface{}
				_ = json.Unmarshal(bodyBytes, &m)
				g.Assert(err).IsNil()
				g.Assert(len(entities)).Eql(12, "expected 10 entities plus @context and @continuation")
				g.Assert(entities[1].ID).Eql("ns3:1")
				token := m[11]["token"].(string)

				// read next page
				res, err = http.Get(dsURL + "/entities?limit=90&from=" + url.QueryEscape(token))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				bodyBytes, _ = io.ReadAll(res.Body)
				_ = res.Body.Close()
				err = json.Unmarshal(bodyBytes, &entities)
				_ = json.Unmarshal(bodyBytes, &m)
				g.Assert(err).IsNil()
				g.Assert(len(entities)).Eql(92, "expected 90 entities plus @context and @continuation")
				g.Assert(entities[1].ID).Eql("ns3:11")
				token = m[91]["token"].(string)

				// read next page after all consumed
				res, err = http.Get(dsURL + "/entities?limit=10&from=" + url.QueryEscape(token))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				bodyBytes, _ = io.ReadAll(res.Body)
				_ = res.Body.Close()
				err = json.Unmarshal(bodyBytes, &entities)
				_ = json.Unmarshal(bodyBytes, &m)
				g.Assert(err).IsNil()
				g.Assert(len(entities)).Eql(2, "expected 0 entities plus @context and @continuation")
				g.Assert(entities[1].ID).Eql("@continuation")
			})
		})
		g.Describe("The /changes and /entities endpoints for proxy datasets", func() {
			g.It("Should fetch from remote for GET /changes without token", func() {
				res, err := http.Get(proxyDsURL + "/changes")
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				b, _ := io.ReadAll(res.Body)
				var entities []*server.Entity
				err = json.Unmarshal(b, &entities)
				g.Assert(err).IsNil()
				g.Assert(len(entities)).Eql(12, "context, 10 entities and continuation")
				g.Assert(entities[1].ID).Eql("ns4:c-0", "first page id range starts with 0")
				g.Assert(mockLayer.RecordedURI).Eql("/datasets/tomatoes/changes")
				var m []map[string]interface{}
				_ = json.Unmarshal(b, &m)
				g.Assert(m[11]["token"]).Eql("nextplease")
			})
			g.It("Should fetch from remote for GET /changes with token and limit", func() {
				res, err := http.Get(proxyDsURL + "/changes?since=theweekend&limit=3")
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				b, _ := io.ReadAll(res.Body)
				var entities []*server.Entity
				err = json.Unmarshal(b, &entities)
				g.Assert(err).IsNil()
				g.Assert(len(entities)).Eql(5, "context, 3 entities and continuation")
				g.Assert(entities[1].ID).Eql("ns4:c-100", "later page mock id range starts with 100")
				g.Assert(mockLayer.RecordedURI).Eql("/datasets/tomatoes/changes?limit=3&since=theweekend")
				var m []map[string]interface{}
				_ = json.Unmarshal(b, &m)
				g.Assert(m[4]["token"]).Eql("lastpage")
				g.Assert(m[0]["namespaces"]).Eql(map[string]interface{}{
					"ns0": "http://data.mimiro.io/core/dataset/",
					"ns1": "http://data.mimiro.io/core/",
					"ns2": "http://www.w3.org/1999/02/22-rdf-syntax-ns#",
					"ns3": "http://example.com",
					"ns4": "http://example.mimiro.io/",
				})
			})
			g.It("Should fetch from remote for GET /entities", func() {
				res, err := http.Get(proxyDsURL + "/entities?from=theweekend&limit=3")
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				b, _ := io.ReadAll(res.Body)
				var entities []*server.Entity
				err = json.Unmarshal(b, &entities)
				g.Assert(err).IsNil()
				g.Assert(len(entities)).
					Eql(11, "context, 10 entities (remote ignored limit) and no continuation (none returned from remote)")
				g.Assert(entities[1].ID).Eql("ns4:e-0")
				g.Assert(mockLayer.RecordedURI).Eql("/datasets/tomatoes/entities?from=theweekend&limit=3")
				var m []map[string]interface{}
				_ = json.Unmarshal(b, &m)
				g.Assert(m[0]["namespaces"]).Eql(map[string]interface{}{
					"ns0": "http://data.mimiro.io/core/dataset/",
					"ns1": "http://data.mimiro.io/core/",
					"ns2": "http://www.w3.org/1999/02/22-rdf-syntax-ns#",
					"ns3": "http://example.com",
					"ns4": "http://example.mimiro.io/",
				})
			})
			g.It("Should push to remote for POST /entities", func() {
				payload := strings.NewReader(bananasFromTo(1, 3, false))
				ctx, cancel := context.WithTimeout(context.Background(), 1000*time.Millisecond)
				req, _ := http.NewRequestWithContext(ctx, "POST", proxyDsURL+"/entities", payload)
				res, err := http.DefaultClient.Do(req)
				cancel()
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				recorded := mockLayer.RecordedEntities["tomatoes"]
				g.Assert(len(recorded)).Eql(4, "context and 3 entities")
				g.Assert(recorded[0].ID).Eql("@context")
				g.Assert(recorded[1].ID).Eql("1")
				g.Assert(recorded[2].ID).Eql("2")
				g.Assert(recorded[3].ID).Eql("3")
				var m []map[string]interface{}
				_ = json.Unmarshal(mockLayer.RecordedBytes["tomatoes"], &m)
				g.Assert(m[0]["namespaces"]).Eql(map[string]interface{}{"_": "http://example.com"})
			})
			g.It("Should forward fullsync headers for POST /entities", func() {
				payload := strings.NewReader(bananasFromTo(1, 17, false))
				ctx, cancel := context.WithTimeout(context.Background(), 1000*time.Millisecond)
				req, _ := http.NewRequestWithContext(ctx, "POST", proxyDsURL+"/entities", payload)
				req.Header.Add("universal-data-api-full-sync-start", "true")
				req.Header.Add("universal-data-api-full-sync-id", "46")
				req.Header.Add("universal-data-api-full-sync-end", "true")
				res, err := http.DefaultClient.Do(req)
				cancel()
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				recorded := mockLayer.RecordedEntities["tomatoes"]
				g.Assert(mockLayer.RecordedHeaders.Get("universal-data-api-full-sync-start")).Eql("true")
				g.Assert(mockLayer.RecordedHeaders.Get("universal-data-api-full-sync-id")).Eql("46")
				g.Assert(mockLayer.RecordedHeaders.Get("universal-data-api-full-sync-end")).Eql("true")
				g.Assert(len(recorded)).Eql(18, "context and 17 entities")
			})
			g.It("Should expose publicNamespaces if configured on proxy dataset", func() {
				// delete proxy ds
				req, _ := http.NewRequest("DELETE", proxyDsURL, nil)
				res, err := http.DefaultClient.Do(req)
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				// make sure it's gone
				res, err = http.Get(proxyDsURL)
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(404)

				// recreate with publicNamespaces
				res, err = http.Post(proxyDsURL+"?proxy=true", "application/json", strings.NewReader(
					`{
							"proxyDatasetConfig": {
								"remoteUrl": "http://localhost:7778/datasets/tomatoes",
                            	"authProviderName": "local"
                         	},
							"publicNamespaces": ["http://example.com", "http://example.mimiro.io/"]
						}`))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				// read it back, hopefully with with publicNamespaces applied
				res, err = http.Get(proxyDsURL + "/changes?limit=1")
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				b, _ := io.ReadAll(res.Body)
				var entities []*server.Entity
				err = json.Unmarshal(b, &entities)
				g.Assert(err).IsNil()
				g.Assert(len(entities)).Eql(3, "context, 1 entity and continuation")
				g.Assert(entities[1].ID).Eql("ns4:c-0")
				g.Assert(mockLayer.RecordedURI).Eql("/datasets/tomatoes/changes?limit=1")
				var m []map[string]interface{}
				_ = json.Unmarshal(b, &m)
				g.Assert(m[0]["namespaces"]).Eql(map[string]interface{}{
					"ns3": "http://example.com",
					"ns4": "http://example.mimiro.io/",
				})
				g.Assert(mockLayer.RecordedHeaders.Get("Authorization")).IsZero(
					"there is no authProvider, fallback to unauthed")
			})

			g.It("Should apply authProvider if configured", func() {
				payload := strings.NewReader(`{
					"name": "local",
					"type": "basic",
					"user": { "value": "u0", "type":"text" },
					"password": { "value":"u1","type":"text"}
				}`)
				req, _ := http.NewRequest("POST", "http://localhost:24997/provider/logins", payload)
				req.Header = http.Header{
					"Content-Type": []string{"application/json"},
				}
				res, err := http.DefaultClient.Do(req)
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				// read changes and verify auth is applied
				res, err = http.Get(proxyDsURL + "/changes?limit=1")
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				b, _ := io.ReadAll(res.Body)
				var entities []*server.Entity
				err = json.Unmarshal(b, &entities)
				g.Assert(err).IsNil()
				g.Assert(len(entities)).Eql(3, "context, 1 entity and continuation")
				g.Assert(entities[1].ID).Eql("ns4:c-0")
				g.Assert(mockLayer.RecordedURI).Eql("/datasets/tomatoes/changes?limit=1")
				g.Assert(mockLayer.RecordedHeaders.Get("Authorization")).Eql("Basic dTA6dTE=",
					"basic auth header expected")
			})
		})
		g.Describe("the /query endpoint", func() {
			g.It("can find changes", func() {
				// relying on dataset being populated from previous cases
				// do query
				js := `
					function do_query() {
						const changes = GetDatasetChanges("bananas")
						let obj = { "bananaCount": changes.Entities.length };
						WriteQueryResult(obj);
					}
					`
				queryEncoded := base64.StdEncoding.EncodeToString([]byte(js))

				query := map[string]any{"query": queryEncoded}
				queryBytes, _ := json.Marshal(query)

				res, err := http.Post(queryURL, "application/x-javascript-query", bytes.NewReader(queryBytes))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				body, _ := io.ReadAll(res.Body)
				g.Assert(string(body)).Eql("[{\"bananaCount\":100}]")
			})
			g.It("can find single ids", func() {
				query := map[string]any{"entityId": "ns3:16"}
				queryBytes, _ := json.Marshal(query)
				res, err := http.Post(queryURL, "application/javascript", bytes.NewReader(queryBytes))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				body, _ := io.ReadAll(res.Body)
				var rMap []map[string]any
				err = json.Unmarshal(body, &rMap)
				g.Assert(err).IsNil()
				g.Assert(rMap).IsNotZero()
				g.Assert(rMap[1]["id"]).Eql("ns3:16")
				g.Assert(rMap[1]["recorded"]).IsNotZero()
			})
			g.It("can find outgoing relations from startUri", func() {
				payload := strings.NewReader(bananaRelations(
					bananaRel{fromBanana: 1, toBananas: []int{2, 3}},
					bananaRel{fromBanana: 2, toBananas: []int{3, 4, 5, 6, 7}},
				))
				res, err := http.Post(dsURL+"/entities", "application/json", payload)
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				query := map[string]any{"startingEntities": []string{"ns3:2"}, "predicate": "*"}
				queryBytes, _ := json.Marshal(query)
				res, err = http.Post(queryURL, "application/javascript", bytes.NewReader(queryBytes))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				body, _ := io.ReadAll(res.Body)
				var rArr []any
				err = json.Unmarshal(body, &rArr)
				g.Assert(err).IsNil()
				g.Assert(rArr).IsNotZero()
				result := rArr[1].([]any)
				g.Assert(len(result)).Eql(5)
				g.Assert(result[4].([]any)[2].(map[string]any)["id"]).Eql("ns3:3")
				g.Assert(result[3].([]any)[2].(map[string]any)["id"]).Eql("ns3:4")
				g.Assert(result[2].([]any)[2].(map[string]any)["id"]).Eql("ns3:5")
				g.Assert(result[1].([]any)[2].(map[string]any)["id"]).Eql("ns3:6")
				g.Assert(result[0].([]any)[2].(map[string]any)["id"]).Eql("ns3:7")
			})
			g.It("can page through queried outgoing relations", func() {
				payload := strings.NewReader(bananaRelations(
					bananaRel{fromBanana: 1, toBananas: []int{2, 3}},
					bananaRel{fromBanana: 2, toBananas: []int{3, 4, 5, 6, 7}},
				))
				res, err := http.Post(dsURL+"/entities", "application/json", payload)
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				query := map[string]any{"startingEntities": []string{"ns3:2"}, "predicate": "*", "limit": 2}
				queryBytes, _ := json.Marshal(query)
				res, err = http.Post(queryURL, "application/javascript", bytes.NewReader(queryBytes))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				body, _ := io.ReadAll(res.Body)
				var rArr []any
				err = json.Unmarshal(body, &rArr)
				g.Assert(err).IsNil()
				g.Assert(rArr).IsNotZero()
				result := rArr[1].([]any)
				g.Assert(len(result)).Eql(2)
				g.Assert(result[1].([]any)[2].(map[string]any)["id"]).Eql("ns3:6")
				g.Assert(result[0].([]any)[2].(map[string]any)["id"]).Eql("ns3:7")
				cont := rArr[2]
				query = map[string]any{"continuations": cont, "limit": 2}
				queryBytes, _ = json.Marshal(query)
				res, err = http.Post(queryURL, "application/javascript", bytes.NewReader(queryBytes))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				body, _ = io.ReadAll(res.Body)
				rArr = []any{}
				err = json.Unmarshal(body, &rArr)
				g.Assert(err).IsNil()
				g.Assert(rArr).IsNotZero()
				result = rArr[1].([]any)
				g.Assert(len(result)).Eql(2)
				g.Assert(result[1].([]any)[2].(map[string]any)["id"]).Eql("ns3:4")
				g.Assert(result[0].([]any)[2].(map[string]any)["id"]).Eql("ns3:5")

				cont = rArr[2]
				query = map[string]any{"continuations": cont, "limit": 2}
				queryBytes, _ = json.Marshal(query)
				res, err = http.Post(queryURL, "application/javascript", bytes.NewReader(queryBytes))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				body, _ = io.ReadAll(res.Body)
				rArr = []any{}
				err = json.Unmarshal(body, &rArr)
				g.Assert(err).IsNil()
				g.Assert(rArr).IsNotZero()
				result = rArr[1].([]any)
				g.Assert(len(result)).Eql(1)
				g.Assert(result[0].([]any)[2].(map[string]any)["id"]).Eql("ns3:3")
				cont = rArr[2]
				g.Assert(len(cont.([]any))).Eql(0)
			})
			g.It("can find inverse relations from startUri", func() {
				payload := strings.NewReader(bananaRelations(
					bananaRel{fromBanana: 1, toBananas: []int{2, 3}},
					bananaRel{fromBanana: 2, toBananas: []int{3, 4}},
					bananaRel{fromBanana: 4, toBananas: []int{3, 2, 1}},
				))
				res, err := http.Post(dsURL+"/entities", "application/json", payload)
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				query := map[string]any{"startingEntities": []string{"ns3:3"}, "predicate": "*", "inverse": true}
				queryBytes, _ := json.Marshal(query)
				res, err = http.Post(queryURL, "application/javascript", bytes.NewReader(queryBytes))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				body, _ := io.ReadAll(res.Body)
				var rArr []any
				err = json.Unmarshal(body, &rArr)
				g.Assert(err).IsNil()
				g.Assert(rArr).IsNotZero()
				result := rArr[1].([]any)
				g.Assert(len(result)).Eql(3)
				g.Assert(result[0].([]any)[2].(map[string]any)["id"]).Eql("ns3:1")
				g.Assert(result[1].([]any)[2].(map[string]any)["id"]).Eql("ns3:2")
				g.Assert(result[2].([]any)[2].(map[string]any)["id"]).Eql("ns3:4")
			})

			g.It("can page through queried inverse relations", func() {
				payload := strings.NewReader(bananaRelations(
					bananaRel{fromBanana: 1, toBananas: []int{2, 3}},
					bananaRel{fromBanana: 2, toBananas: []int{3, 4}},
					bananaRel{fromBanana: 3, toBananas: []int{2, 1}},
					bananaRel{fromBanana: 4, toBananas: []int{3, 2, 1}},
					bananaRel{fromBanana: 5, toBananas: []int{3, 2, 1}},
					bananaRel{fromBanana: 6, toBananas: []int{3, 2, 1}},
					bananaRel{fromBanana: 7, toBananas: []int{3, 2, 1}},
				))
				res, err := http.Post(dsURL+"/entities", "application/json", payload)
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)

				query := map[string]any{
					"startingEntities": []string{"ns3:3"},
					"predicate":        "*",
					"inverse":          true,
					"limit":            2,
				}
				queryBytes, _ := json.Marshal(query)
				res, err = http.Post(queryURL, "application/javascript", bytes.NewReader(queryBytes))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				body, _ := io.ReadAll(res.Body)
				var rArr []any
				err = json.Unmarshal(body, &rArr)
				g.Assert(err).IsNil()
				g.Assert(rArr).IsNotZero()
				result := rArr[1].([]any)
				g.Assert(len(result)).Eql(2)
				g.Assert(result[0].([]any)[2].(map[string]any)["id"]).Eql("ns3:1")
				g.Assert(result[1].([]any)[2].(map[string]any)["id"]).Eql("ns3:2")

				cont := rArr[2]
				savedCont := cont
				query = map[string]any{"continuations": cont, "limit": 2}
				queryBytes, _ = json.Marshal(query)
				res, err = http.Post(queryURL, "application/javascript", bytes.NewReader(queryBytes))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				body, _ = io.ReadAll(res.Body)
				rArr = []any{}
				err = json.Unmarshal(body, &rArr)
				g.Assert(err).IsNil()
				g.Assert(rArr).IsNotZero()
				result = rArr[1].([]any)
				g.Assert(len(result)).Eql(2)
				g.Assert(result[0].([]any)[2].(map[string]any)["id"]).Eql("ns3:4")
				g.Assert(result[1].([]any)[2].(map[string]any)["id"]).Eql("ns3:5")

				cont = rArr[2]
				query = map[string]any{"continuations": cont, "limit": 2}
				queryBytes, _ = json.Marshal(query)
				res, err = http.Post(queryURL, "application/javascript", bytes.NewReader(queryBytes))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				body, _ = io.ReadAll(res.Body)
				rArr = []any{}
				err = json.Unmarshal(body, &rArr)
				g.Assert(err).IsNil()
				g.Assert(rArr).IsNotZero()
				result = rArr[1].([]any)
				g.Assert(len(result)).Eql(2)
				g.Assert(result[0].([]any)[2].(map[string]any)["id"]).Eql("ns3:6")
				g.Assert(result[1].([]any)[2].(map[string]any)["id"]).Eql("ns3:7")

				// go through last pages with different batch sizes
				query = map[string]any{"continuations": savedCont, "limit": 3}
				queryBytes, _ = json.Marshal(query)
				res, err = http.Post(queryURL, "application/javascript", bytes.NewReader(queryBytes))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				body, _ = io.ReadAll(res.Body)
				rArr = []any{}
				err = json.Unmarshal(body, &rArr)
				g.Assert(err).IsNil()
				g.Assert(rArr).IsNotZero()
				result = rArr[1].([]any)
				g.Assert(len(result)).Eql(3)
				g.Assert(result[0].([]any)[2].(map[string]any)["id"]).Eql("ns3:4")
				g.Assert(result[1].([]any)[2].(map[string]any)["id"]).Eql("ns3:5")
				g.Assert(result[2].([]any)[2].(map[string]any)["id"]).Eql("ns3:6")

				cont = rArr[2]
				query = map[string]any{"continuations": cont, "limit": 2}
				queryBytes, _ = json.Marshal(query)
				res, err = http.Post(queryURL, "application/javascript", bytes.NewReader(queryBytes))
				g.Assert(err).IsNil()
				g.Assert(res).IsNotZero()
				g.Assert(res.StatusCode).Eql(200)
				body, _ = io.ReadAll(res.Body)
				rArr = []any{}
				err = json.Unmarshal(body, &rArr)
				g.Assert(err).IsNil()
				g.Assert(rArr).IsNotZero()
				result = rArr[1].([]any)
				g.Assert(len(result)).Eql(1)
				g.Assert(result[0].([]any)[2].(map[string]any)["id"]).Eql("ns3:7")
			})
		})
	})
}

func bananaRelations(rels ...bananaRel) string {
	prefix := `[ { "id" : "@context", "namespaces" : { "_" : "http://example.com" } }, `

	var bananas []string

	for _, rel := range rels {
		refStr := ""
		for i, rStr := range rel.toBananas {
			refStr = refStr + fmt.Sprintf(`"%v"`, rStr)
			if i < len(rel.toBananas)-1 {
				refStr = refStr + ","
			}
		}
		bananas = append(bananas, fmt.Sprintf(`{ "id" : "%v", "refs": {"link": [%v]} }`, rel.fromBanana, refStr))
	}

	return prefix + strings.Join(bananas, ",") + "]"
}

func bananasFromTo(from, to int, deleted bool) string {
	prefix := `[ { "id" : "@context", "namespaces" : { "_" : "http://example.com" } }, `

	var bananas []string

	for i := from; i <= to; i++ {
		if deleted {
			bananas = append(bananas, fmt.Sprintf(`{ "id" : "%v", "deleted": true }`, i))
		} else {
			bananas = append(bananas, fmt.Sprintf(`{ "id" : "%v" }`, i))
		}
	}

	return prefix + strings.Join(bananas, ",") + "]"
}

type bananaRel struct {
	fromBanana int
	toBananas  []int
}

type MockLayer struct {
	RecordedEntities map[string][]*server.Entity
	echo             *echo.Echo
	RecordedURI      string
	RecordedBytes    map[string][]byte
	RecordedHeaders  http.Header
}

type Continuation struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

func NewMockLayer() *MockLayer {
	e := echo.New()
	result := &MockLayer{}
	result.RecordedEntities = make(map[string][]*server.Entity)
	result.RecordedBytes = make(map[string][]byte)
	result.echo = e
	e.HideBanner = true

	ctx := make(map[string]interface{})
	ctx["id"] = "@context"
	ns := make(map[string]string)
	ns["ex"] = "http://example.mimiro.io/"
	ns["_"] = "http://default.mimiro.io/"
	ctx["namespaces"] = ns

	e.POST("/datasets/tomatoes/entities", func(context echo.Context) error {
		b, _ := io.ReadAll(context.Request().Body)
		entities := []*server.Entity{}
		_ = json.Unmarshal(b, &entities)
		result.RecordedEntities["tomatoes"] = entities
		result.RecordedBytes["tomatoes"] = b
		result.RecordedHeaders = context.Request().Header
		return context.NoContent(http.StatusOK)
	})
	e.GET("/datasets/tomatoes/entities", func(context echo.Context) error {
		r := make([]interface{}, 0)
		r = append(r, ctx)
		result.RecordedURI = context.Request().RequestURI
		result.RecordedHeaders = context.Request().Header

		// add some objects
		for i := 0; i < 10; i++ {
			e := server.NewEntity("ex:e-"+strconv.Itoa(i), 0)
			r = append(r, e)
		}
		return context.JSON(http.StatusOK, r)
	})

	e.GET("/datasets/tomatoes/changes", func(context echo.Context) error {
		r := make([]interface{}, 0)
		r = append(r, ctx)
		result.RecordedURI = context.Request().RequestURI
		result.RecordedHeaders = context.Request().Header

		// check for since
		since := context.QueryParam("since")
		l := context.QueryParam("limit")
		cnt := 10
		if limit, ok := strconv.Atoi(l); ok == nil {
			cnt = limit
		}
		switch since {
		case "":
			// add some objects
			for i := 0; i < cnt; i++ {
				e := server.NewEntity("ex:c-"+strconv.Itoa(i), 0)
				r = append(r, e)
			}
			c := &Continuation{ID: "@continuation", Token: "nextplease"}
			r = append(r, c)
		case "lastpage":
			c := &Continuation{ID: "@continuation", Token: "lastpage"}
			r = append(r, c)
		default:
			// return more objects
			for i := 100; i < 100+cnt; i++ {
				e := server.NewEntity("ex:c-"+strconv.Itoa(i), 0)
				r = append(r, e)
			}
			c := &Continuation{ID: "@continuation", Token: "lastpage"}
			r = append(r, c)
		}

		return context.JSON(http.StatusOK, r)
	})

	return result
}
