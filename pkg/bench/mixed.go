/*
 * Warp (C) 2019-2020 MinIO, Inc.
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

package bench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/minio/mc/pkg/console"
	"github.com/minio/minio-go/v6"
	"github.com/minio/warp/pkg/generator"
)

// Mixed benchmarks download speed.
type Mixed struct {
	CreateObjects int
	Collector     *Collector
	Dist          *MixedDistribution

	GetOpts  minio.GetObjectOptions
	StatOpts minio.StatObjectOptions
	Common
}

type MixedDistribution struct {
	// Operation -> distribution.
	Distribution map[string]float64
	ops          []string
	objects      map[string]generator.Object
	rng          *rand.Rand

	current int
	mu      sync.Mutex
}

func (m *MixedDistribution) Generate(allocObjs int) error {
	if m.Distribution["DELETE"] > m.Distribution["PUT"] {
		return errors.New("DELETE distribution cannot be bigger than PUT")
	}
	m.objects = make(map[string]generator.Object, allocObjs)

	err := m.normalize()
	if err != nil {
		return err
	}

	const genOps = 1000
	m.ops = make([]string, 0, genOps)
	for op, dist := range m.Distribution {
		add := int(0.5 + dist*genOps)
		for i := 0; i < add; i++ {
			m.ops = append(m.ops, op)
		}
	}
	m.rng = rand.New(rand.NewSource(0xabad1dea))
	sort.Slice(m.ops, func(i, j int) bool {
		return m.rng.Int63()&1 == 0
	})
	return nil
}

func (m *MixedDistribution) normalize() error {
	total := 0.0
	for op, dist := range m.Distribution {
		if dist < 0 {
			return fmt.Errorf("negative distribution requested for op %q", op)
		}
		total += dist
	}
	if total == 0 {
		return fmt.Errorf("no distribution set, total is 0")
	}
	for op, dist := range m.Distribution {
		m.Distribution[op] = dist / total
	}
	return nil
}

func (m *MixedDistribution) randomObj() generator.Object {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Use map randomness to select.
	for _, o := range m.objects {
		return o
	}
	panic("ran out of objects")
}

func (m *MixedDistribution) deleteRandomObj() generator.Object {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Use map randomness to select.
	for k, o := range m.objects {
		delete(m.objects, k)
		return o
	}
	panic("ran out of objects")
}

func (m *MixedDistribution) addObj(o generator.Object) {
	m.mu.Lock()
	m.objects[o.Name] = o
	m.mu.Unlock()
}

func (m *MixedDistribution) getOp() string {
	m.mu.Lock()
	op := m.ops[m.current]
	m.current = (m.current + 1) % len(m.ops)
	m.mu.Unlock()
	return op
}

// Prepare will create an empty bucket or delete any content already there
// and upload a number of objects.
func (g *Mixed) Prepare(ctx context.Context) error {
	if err := g.createEmptyBucket(ctx); err != nil {
		return err
	}
	src := g.Source()
	console.Infoln("Uploading", g.CreateObjects, "Objects of", src.String())
	var wg sync.WaitGroup
	wg.Add(g.Concurrency)
	g.Collector = NewCollector()
	obj := make(chan struct{}, g.CreateObjects)
	for i := 0; i < g.CreateObjects; i++ {
		obj <- struct{}{}
	}
	close(obj)
	var groupErr error
	var mu sync.Mutex
	for i := 0; i < g.Concurrency; i++ {
		go func(i int) {
			defer wg.Done()
			src := g.Source()
			for range obj {
				opts := g.PutOpts
				done := ctx.Done()

				select {
				case <-done:
					return
				default:
				}
				obj := src.Object()
				client, cldone := g.Client()
				opts.ContentType = obj.ContentType
				n, err := client.PutObject(g.Bucket, obj.Name, obj.Reader, obj.Size, opts)
				if err != nil {
					err := fmt.Errorf("upload error: %w", err)
					console.Error(err)
					mu.Lock()
					if groupErr == nil {
						groupErr = err
					}
					mu.Unlock()
					return
				}
				if n != obj.Size {
					err := fmt.Errorf("short upload. want: %d, got %d", obj.Size, n)
					console.Error(err)
					mu.Lock()
					if groupErr == nil {
						groupErr = err
					}
					mu.Unlock()
					return
				}
				cldone()
				obj.Reader = nil
				g.Dist.addObj(*obj)
				g.prepareProgress(float64(len(g.Dist.objects)) / float64(g.CreateObjects))
			}
		}(i)
	}
	wg.Wait()
	return groupErr
}

// Start will execute the main benchmark.
// Operations should begin executing when the start channel is closed.
func (g *Mixed) Start(ctx context.Context, wait chan struct{}) (Operations, error) {
	var wg sync.WaitGroup
	wg.Add(g.Concurrency)
	c := g.Collector
	if g.AutoTermDur > 0 {
		ctx = c.AutoTerm(ctx, "MIXED", g.AutoTermScale, autoTermCheck, autoTermSamples, g.AutoTermDur)
	}
	for i := 0; i < g.Concurrency; i++ {
		go func(i int) {
			rcv := c.Receiver()
			defer wg.Done()
			done := ctx.Done()
			src := g.Source()
			putOpts := g.PutOpts
			statOpts := g.StatOpts

			<-wait
			for {
				select {
				case <-done:
					return
				default:
				}
				operation := g.Dist.getOp()
				switch operation {
				case "GET":
					fbr := firstByteRecorder{}
					obj := g.Dist.randomObj()
					client, cldone := g.Client()
					op := Operation{
						OpType:   operation,
						Thread:   uint16(i),
						Size:     obj.Size,
						File:     obj.Name,
						ObjPerOp: 1,
						Endpoint: client.EndpointURL().String(),
					}
					op.Start = time.Now()
					var err error
					fbr.r, err = client.GetObject(g.Bucket, obj.Name, g.GetOpts)
					if err != nil {
						console.Errorln("download error:", err)
						op.Err = err.Error()
						op.End = time.Now()
						rcv <- op
						cldone()
						continue
					}
					n, err := io.Copy(ioutil.Discard, &fbr)
					if err != nil {
						console.Errorln("download error:", err)
						op.Err = err.Error()
					}
					op.FirstByte = fbr.t
					op.End = time.Now()
					if n != obj.Size && op.Err == "" {
						op.Err = fmt.Sprint("unexpected download size. want:", obj.Size, ", got:", n)
						console.Errorln(op.Err)
					}
					rcv <- op
					cldone()
				case "PUT":
					obj := src.Object()
					putOpts.ContentType = obj.ContentType
					client, cldone := g.Client()
					op := Operation{
						OpType:   operation,
						Thread:   uint16(i),
						Size:     obj.Size,
						File:     obj.Name,
						ObjPerOp: 1,
						Endpoint: client.EndpointURL().String(),
					}
					op.Start = time.Now()
					n, err := client.PutObject(g.Bucket, obj.Name, obj.Reader, obj.Size, putOpts)
					op.End = time.Now()
					if err != nil {
						console.Errorln("upload error:", err)
						op.Err = err.Error()
					}
					if n != obj.Size {
						err := fmt.Sprint("short upload. want:", obj.Size, ", got:", n)
						if op.Err == "" {
							op.Err = err
						}
						console.Errorln(err)
					}
					cldone()
					if op.Err == "" {
						g.Dist.addObj(*obj)
					}
					rcv <- op
				case "DELETE":
					client, cldone := g.Client()
					obj := g.Dist.deleteRandomObj()
					op := Operation{
						OpType:   operation,
						Thread:   uint16(i),
						Size:     0,
						File:     obj.Name,
						ObjPerOp: 1,
						Endpoint: client.EndpointURL().String(),
					}
					op.Start = time.Now()
					err := client.RemoveObject(g.Bucket, obj.Name)
					op.End = time.Now()
					cldone()
					if err != nil {
						console.Errorln("delete error:", err)
						op.Err = err.Error()
					}
					rcv <- op
				case "STAT":
					obj := g.Dist.randomObj()
					client, cldone := g.Client()
					op := Operation{
						OpType:   operation,
						Thread:   uint16(i),
						Size:     0,
						File:     obj.Name,
						ObjPerOp: 1,
						Endpoint: client.EndpointURL().String(),
					}
					op.Start = time.Now()
					var err error
					objI, err := client.StatObject(g.Bucket, obj.Name, statOpts)
					if err != nil {
						console.Errorln("stat error:", err)
						op.Err = err.Error()
					}
					op.End = time.Now()
					if objI.Size != obj.Size && op.Err == "" {
						op.Err = fmt.Sprint("unexpected stat size. want:", obj.Size, ", got:", objI.Size)
						console.Errorln(op.Err)
					}
					rcv <- op
					cldone()
				default:
					console.Errorln("unknown operation:", operation)
				}
			}
		}(i)
	}
	wg.Wait()
	return c.Close(), nil
}

// Cleanup deletes everything uploaded to the bucket.
func (g *Mixed) Cleanup(ctx context.Context) {
	g.deleteAllInBucket(ctx)
}
