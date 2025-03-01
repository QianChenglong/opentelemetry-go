// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package otlpmetrichttp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/internal/otest"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestClient(t *testing.T) {
	factory := func() (otlpmetric.Client, otest.Collector) {
		coll, err := otest.NewHTTPCollector("", nil)
		require.NoError(t, err)

		addr := coll.Addr().String()
		client, err := newClient(WithEndpoint(addr), WithInsecure())
		require.NoError(t, err)
		return client, coll
	}

	t.Run("Integration", otest.RunClientTests(factory))
}

func TestConfig(t *testing.T) {
	factoryFunc := func(ePt string, errCh <-chan error, o ...Option) (metric.Exporter, *otest.HTTPCollector) {
		coll, err := otest.NewHTTPCollector(ePt, errCh)
		require.NoError(t, err)

		opts := []Option{WithEndpoint(coll.Addr().String())}
		if !strings.HasPrefix(strings.ToLower(ePt), "https") {
			opts = append(opts, WithInsecure())
		}
		opts = append(opts, o...)

		ctx := context.Background()
		exp, err := New(ctx, opts...)
		require.NoError(t, err)
		return exp, coll
	}

	t.Run("WithHeaders", func(t *testing.T) {
		key := http.CanonicalHeaderKey("my-custom-header")
		headers := map[string]string{key: "custom-value"}
		exp, coll := factoryFunc("", nil, WithHeaders(headers))
		ctx := context.Background()
		t.Cleanup(func() { require.NoError(t, coll.Shutdown(ctx)) })
		require.NoError(t, exp.Export(ctx, metricdata.ResourceMetrics{}))
		// Ensure everything is flushed.
		require.NoError(t, exp.Shutdown(ctx))

		got := coll.Headers()
		require.Contains(t, got, key)
		assert.Equal(t, got[key], []string{headers[key]})
	})

	t.Run("WithTimeout", func(t *testing.T) {
		// Do not send on errCh so the Collector never responds to the client.
		errCh := make(chan error)
		exp, coll := factoryFunc(
			"",
			errCh,
			WithTimeout(time.Millisecond),
			WithRetry(RetryConfig{Enabled: false}),
		)
		ctx := context.Background()
		t.Cleanup(func() { require.NoError(t, coll.Shutdown(ctx)) })
		// Push this after Shutdown so the HTTP server doesn't hang.
		t.Cleanup(func() { close(errCh) })
		t.Cleanup(func() { require.NoError(t, exp.Shutdown(ctx)) })
		err := exp.Export(ctx, metricdata.ResourceMetrics{})
		assert.ErrorContains(t, err, context.DeadlineExceeded.Error())
	})

	t.Run("WithCompressionGZip", func(t *testing.T) {
		exp, coll := factoryFunc("", nil, WithCompression(GzipCompression))
		ctx := context.Background()
		t.Cleanup(func() { require.NoError(t, coll.Shutdown(ctx)) })
		t.Cleanup(func() { require.NoError(t, exp.Shutdown(ctx)) })
		assert.NoError(t, exp.Export(ctx, metricdata.ResourceMetrics{}))
		assert.Len(t, coll.Collect().Dump(), 1)
	})

	t.Run("WithRetry", func(t *testing.T) {
		emptyErr := errors.New("")
		errCh := make(chan error, 3)
		header := http.Header{http.CanonicalHeaderKey("Retry-After"): {"10"}}
		// Both retryable errors.
		errCh <- &otest.HTTPResponseError{Status: http.StatusServiceUnavailable, Err: emptyErr, Header: header}
		errCh <- &otest.HTTPResponseError{Status: http.StatusTooManyRequests, Err: emptyErr}
		errCh <- nil
		exp, coll := factoryFunc("", errCh, WithRetry(RetryConfig{
			Enabled:         true,
			InitialInterval: time.Nanosecond,
			MaxInterval:     time.Millisecond,
			MaxElapsedTime:  time.Minute,
		}))
		ctx := context.Background()
		t.Cleanup(func() { require.NoError(t, coll.Shutdown(ctx)) })
		// Push this after Shutdown so the HTTP server doesn't hang.
		t.Cleanup(func() { close(errCh) })
		t.Cleanup(func() { require.NoError(t, exp.Shutdown(ctx)) })
		assert.NoError(t, exp.Export(ctx, metricdata.ResourceMetrics{}), "failed retry")
		assert.Len(t, errCh, 0, "failed HTTP responses did not occur")
	})

	t.Run("WithURLPath", func(t *testing.T) {
		path := "/prefix/v2/metrics"
		ePt := fmt.Sprintf("http://localhost:0%s", path)
		exp, coll := factoryFunc(ePt, nil, WithURLPath(path))
		ctx := context.Background()
		t.Cleanup(func() { require.NoError(t, coll.Shutdown(ctx)) })
		t.Cleanup(func() { require.NoError(t, exp.Shutdown(ctx)) })
		assert.NoError(t, exp.Export(ctx, metricdata.ResourceMetrics{}))
		assert.Len(t, coll.Collect().Dump(), 1)
	})

	t.Run("WithURLPath", func(t *testing.T) {
		path := "/prefix/v2/metrics"
		ePt := fmt.Sprintf("http://localhost:0%s", path)
		exp, coll := factoryFunc(ePt, nil, WithURLPath(path))
		ctx := context.Background()
		t.Cleanup(func() { require.NoError(t, coll.Shutdown(ctx)) })
		t.Cleanup(func() { require.NoError(t, exp.Shutdown(ctx)) })
		assert.NoError(t, exp.Export(ctx, metricdata.ResourceMetrics{}))
		assert.Len(t, coll.Collect().Dump(), 1)
	})

	t.Run("WithTLSClientConfig", func(t *testing.T) {
		ePt := "https://localhost:0"
		tlsCfg := &tls.Config{InsecureSkipVerify: true}
		exp, coll := factoryFunc(ePt, nil, WithTLSClientConfig(tlsCfg))
		ctx := context.Background()
		t.Cleanup(func() { require.NoError(t, coll.Shutdown(ctx)) })
		t.Cleanup(func() { require.NoError(t, exp.Shutdown(ctx)) })
		assert.NoError(t, exp.Export(ctx, metricdata.ResourceMetrics{}))
		assert.Len(t, coll.Collect().Dump(), 1)
	})
}
