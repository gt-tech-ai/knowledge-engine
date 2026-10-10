package unit_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	clientdecorators "github.com/gt-tech-ai/knowledge-engine/go/clients/decorators"
	"github.com/gt-tech-ai/knowledge-engine/go/clients/prometheus"
	coreerr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/mocks"
)

// promMatrix is a successful range-query envelope; %s is replaced by the series list.
const promMatrix = `{"status":"success","data":{"resultType":"matrix","result":[%s]}}`

// promVector is a successful instant-query envelope; %s is replaced by the series list.
const promVector = `{"status":"success","data":{"resultType":"vector","result":[%s]}}`

// promResponse builds an HTTP response with status and body.
func promResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

// promSeries builds one matrix series with a sample per value, an hour apart from t=1727260000.
func promSeries(vals ...string) string {
	pts := make([]string, 0, len(vals))
	for i, v := range vals {
		pts = append(pts, `[`+[]string{"1727260000", "1727263600"}[i]+`,"`+v+`"]`)
	}
	return `{"metric":{},"values":[` + strings.Join(pts, ",") + `]}`
}

// promInstant builds one vector series with a single sample.
func promInstant(v string) string {
	return `{"metric":{},"value":[1727260000,"` + v + `"]}`
}

// TestClient_QueryParsesVector tests the instant query's contract: one finite sample is the
// value; anything else is a coded error.
//
// Why this test is important:
//   - A health or SLO number built from several un-aggregated series, or one that turned "no
//     data" into 0, would report a value Prometheus never computed.
//
// What it tests:
//   - The request is a GET of <base>/api/v1/query (trailing slash trimmed) with the PromQL.
//   - One sample returns its value; no sample and a NaN are CodeNotFound; two series and a
//     non-vector result are CodeInvalidInput.
func TestClient_QueryParsesVector(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		body     string
		wantCode coreerr.ErrorCode
		want     float64
	}{
		{name: "one sample", body: strings.Replace(promVector, "%s", promInstant("12.5"), 1), want: 12.5},
		{name: "no sample", body: strings.Replace(promVector, "%s", "", 1), wantCode: coreerr.CodeNotFound},
		{name: "NaN", body: strings.Replace(promVector, "%s", promInstant("NaN"), 1), wantCode: coreerr.CodeNotFound},
		{
			name:     "two series",
			body:     strings.Replace(promVector, "%s", promInstant("1")+","+promInstant("2"), 1),
			wantCode: coreerr.CodeInvalidInput,
		},
		{name: "matrix", body: strings.Replace(promMatrix, "%s", "", 1), wantCode: coreerr.CodeInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doer := mocks.NewMockPrometheusHTTPDoer(gomock.NewController(t))
			var req *http.Request
			doer.EXPECT().Do(gomock.Any()).DoAndReturn(func(r *http.Request) (*http.Response, error) {
				req = r
				return promResponse(http.StatusOK, tc.body), nil
			})

			got, err := prometheus.New("http://prom.test:9090/", doer).Query(t.Context(), `sum(up)`)

			require.NotNil(t, req)
			assert.Equal(t, http.MethodGet, req.Method)
			assert.Equal(t, "prom.test:9090", req.URL.Host)
			assert.Equal(t, "/api/v1/query", req.URL.Path)
			assert.Equal(t, `sum(up)`, req.URL.Query().Get("query"))
			if tc.wantCode != "" {
				require.Error(t, err)
				assert.Equal(t, tc.wantCode, coreerr.Code(err))
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tc.want, got, 0)
		})
	}
}

// TestClient_QueryRangeParsesMatrix tests the range query's contract: one series of finite
// samples is the series; anything else is an error.
//
// Why this test is important:
//   - A trend chart built from several un-aggregated series, or one that turned NaN into 0,
//     would show a number Prometheus never computed.
//
// What it tests:
//   - The request is a GET of <base>/api/v1/query_range with the PromQL, start, end and step.
//   - A one-series matrix returns its samples in order with their timestamps; an empty matrix
//     is an empty series.
//   - Two series is CodeInvalidInput and a NaN sample CodeNotFound.
func TestClient_QueryRangeParsesMatrix(t *testing.T) {
	t.Parallel()
	t0 := time.Unix(1727260000, 0).UTC()
	cases := []struct {
		name     string
		body     string
		wantCode coreerr.ErrorCode
		want     []prometheus.Sample
	}{
		{
			name: "one series",
			body: strings.Replace(promMatrix, "%s", promSeries("1.5", "2"), 1),
			want: []prometheus.Sample{{Time: t0, Value: 1.5}, {Time: t0.Add(time.Hour), Value: 2}},
		},
		{name: "no series", body: strings.Replace(promMatrix, "%s", "", 1), want: []prometheus.Sample{}},
		{
			name:     "two series",
			body:     strings.Replace(promMatrix, "%s", promSeries("1")+","+promSeries("2"), 1),
			wantCode: coreerr.CodeInvalidInput,
		},
		{name: "NaN", body: strings.Replace(promMatrix, "%s", promSeries("NaN"), 1), wantCode: coreerr.CodeNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doer := mocks.NewMockPrometheusHTTPDoer(gomock.NewController(t))
			var req *http.Request
			doer.EXPECT().Do(gomock.Any()).DoAndReturn(func(r *http.Request) (*http.Response, error) {
				req = r
				return promResponse(http.StatusOK, tc.body), nil
			})

			got, err := prometheus.New("http://prom.test:9090/", doer).QueryRange(
				t.Context(), `sum(increase(x[1h]))`, t0, t0.Add(time.Hour), time.Hour,
			)

			require.NotNil(t, req)
			assert.Equal(t, http.MethodGet, req.Method)
			assert.Equal(t, "/api/v1/query_range", req.URL.Path)
			q := req.URL.Query()
			assert.Equal(t, `sum(increase(x[1h]))`, q.Get("query"))
			assert.Equal(t, "1727260000", q.Get("start"))
			assert.Equal(t, "1727263600", q.Get("end"))
			assert.Equal(t, "3600", q.Get("step"))
			if tc.wantCode != "" {
				require.Error(t, err)
				assert.Equal(t, tc.wantCode, coreerr.Code(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestClient_ErrorStatusIsCoded tests that every failure on the request path carries the
// code that tells a caller whether to retry, fix the query, or report the server.
//
// Why this test is important:
//   - The client stack retries only transient codes; an outage coded as a bad query would
//     never be retried, and a server-side failure coded as transient would be retried forever.
//
// What it tests:
//   - A transport error is CodeUnavailable; an HTTP error status (with the server's message),
//     a "status":"error" envelope, an undecodable body and an unparsable sample are
//     CodeUpstream; a base URL that cannot form a request is CodeInvalidInput with no send.
func TestClient_ErrorStatusIsCoded(t *testing.T) {
	t.Parallel()
	cases := []struct {
		resp     *http.Response
		doErr    error
		name     string
		wantMsg  string
		wantCode coreerr.ErrorCode
	}{
		{name: "transport", doErr: coreerr.Sentinel("connection refused"), wantCode: coreerr.CodeUnavailable},
		{
			name:     "HTTP 400",
			resp:     promResponse(http.StatusBadRequest, `{"status":"error","error":"parse error at char 3"}`),
			wantCode: coreerr.CodeUpstream,
			wantMsg:  "parse error at char 3",
		},
		{
			name:     "status error",
			resp:     promResponse(http.StatusOK, `{"status":"error","error":"query timed out"}`),
			wantCode: coreerr.CodeUpstream,
			wantMsg:  "query timed out",
		},
		{name: "bad json", resp: promResponse(http.StatusBadGateway, `<html>`), wantCode: coreerr.CodeUpstream},
		{
			name:     "bad sample",
			resp:     promResponse(http.StatusOK, strings.Replace(promVector, "%s", promInstant("abc"), 1)),
			wantCode: coreerr.CodeUpstream,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doer := mocks.NewMockPrometheusHTTPDoer(gomock.NewController(t))
			doer.EXPECT().Do(gomock.Any()).Return(tc.resp, tc.doErr)

			_, err := prometheus.New("http://prom.test:9090", doer).Query(t.Context(), `up`)

			require.Error(t, err)
			assert.Equal(t, tc.wantCode, coreerr.Code(err))
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
	t.Run("bad base URL", func(t *testing.T) {
		t.Parallel()
		doer := mocks.NewMockPrometheusHTTPDoer(gomock.NewController(t))
		doer.EXPECT().Do(gomock.Any()).Times(0)

		_, err := prometheus.New("http://[::1", doer).Query(t.Context(), `up`)

		require.Error(t, err)
		assert.Equal(t, coreerr.CodeInvalidInput, coreerr.Code(err))
	})
}

// TestNewFromConfig_StubDefaultAndUnknownKind tests the factory: the default config boots
// the stub with no infrastructure, and a bad selection fails loudly at construction.
//
// Why this test is important:
//   - The graph must boot with zero infrastructure, and a typo in the configured kind must stop
//     the process at startup rather than silently fall back to a backend.
//
// What it tests:
//   - DefaultConfig builds the stub: Query returns 0 and QueryRange an empty series, no error.
//   - An unknown kind and the http kind without a base URL are CodeInvalidInput.
//   - The http kind with a base URL builds a querier without sending anything.
func TestNewFromConfig_StubDefaultAndUnknownKind(t *testing.T) {
	t.Parallel()
	q, err := prometheus.NewFromConfig(prometheus.DefaultConfig(), clientdecorators.Deps{})
	require.NoError(t, err)
	v, err := q.Query(t.Context(), `up`)
	require.NoError(t, err)
	assert.InDelta(t, 0.0, v, 0)
	series, err := q.QueryRange(t.Context(), `up`, time.Unix(0, 0), time.Unix(60, 0), time.Minute)
	require.NoError(t, err)
	assert.Equal(t, []prometheus.Sample{}, series)

	unknown := prometheus.DefaultConfig()
	unknown.Kind = prometheus.Kind(99)
	_, err = prometheus.NewFromConfig(unknown, clientdecorators.Deps{})
	require.Error(t, err)
	assert.Equal(t, coreerr.CodeInvalidInput, coreerr.Code(err))

	noURL := prometheus.DefaultConfig()
	noURL.Kind = prometheus.KindHTTP
	_, err = prometheus.NewFromConfig(noURL, clientdecorators.Deps{})
	require.Error(t, err)
	assert.Equal(t, coreerr.CodeInvalidInput, coreerr.Code(err))

	withURL := noURL
	withURL.BaseURL = "http://prom.invalid:9090"
	q, err = prometheus.NewFromConfig(withURL, clientdecorators.Deps{})
	require.NoError(t, err)
	assert.NotNil(t, q)
}

// TestDecorateDoer_RetriesTransportErrorsAndBuffersBody tests the client-stack wrapper around
// the HTTP transport.
//
// Why this test is important:
//   - The stack's per-attempt timeout ends when an attempt returns; a body read after that
//     would fail, and a transient transport error that is not retried fails a query a second
//     attempt would have answered.
//
// What it tests:
//   - With retries on, a transport error then a response returns that response; the transport
//     body is closed inside the attempt, and the returned body still reads in full.
//   - With retries off, a transport error comes back coded CodeUnavailable.
func TestDecorateDoer_RetriesTransportErrorsAndBuffersBody(t *testing.T) {
	t.Parallel()
	cfg := clientdecorators.DefaultConfig()
	cfg.RetryEnabled = true
	cfg.Retry.InitialInterval = time.Millisecond
	cfg.Retry.MaxInterval = time.Millisecond
	stack, err := clientdecorators.StackFromConfig("prometheus", cfg, clientdecorators.Deps{})
	require.NoError(t, err)
	ctrl := gomock.NewController(t)
	inner := mocks.NewMockPrometheusHTTPDoer(ctrl)
	// The transport body is closed by the wrapper before Do returns; the caller still reads it.
	src := mocks.NewMockReadCloser(ctrl)
	src.EXPECT().Read(gomock.Any()).DoAndReturn(func(p []byte) (int, error) {
		return copy(p, "payload"), io.EOF
	})
	src.EXPECT().Close().Return(nil)
	gomock.InOrder(
		inner.EXPECT().Do(gomock.Any()).Return(nil, coreerr.Sentinel("connection reset")),
		inner.EXPECT().Do(gomock.Any()).Return(&http.Response{StatusCode: http.StatusOK, Body: src}, nil),
	)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://prom.test/api/v1/query", http.NoBody)
	require.NoError(t, err)

	resp, err := prometheus.DecorateDoer(inner, stack).Do(req)

	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(body))

	bare, err := clientdecorators.StackFromConfig("prometheus", clientdecorators.Config{}, clientdecorators.Deps{})
	require.NoError(t, err)
	failing := mocks.NewMockPrometheusHTTPDoer(gomock.NewController(t))
	failing.EXPECT().Do(gomock.Any()).Return(nil, coreerr.Sentinel("connection refused"))
	_, err = prometheus.DecorateDoer(failing, bare).Do(req)
	require.Error(t, err)
	assert.Equal(t, coreerr.CodeUnavailable, coreerr.Code(err))
}
