package engine

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testCORSPath              = "/foo"
	testAllowedOrigin         = "https://example.com"
	testAllowedOriginWithPort = "http://localhost:8080"
	testOtherPortOrigin       = "http://localhost:9090"
	testDisallowedOrigin      = "https://evil.example"
)

func newCORSTestRouter(t *testing.T, enableCORS bool, origins []string) *chi.Mux {
	t.Helper()
	router := newRouter("1.2.3", false, enableCORS, origins)
	router.Get(testCORSPath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return router
}

func TestValidateCORSAllowedOrigins_Valid(t *testing.T) {
	tests := []struct {
		name    string
		origins []string
	}{
		{name: "single http origin", origins: []string{"http://example.com"}},
		{name: "single https origin", origins: []string{"https://example.com"}},
		{name: "origin with port", origins: []string{testAllowedOriginWithPort}},
		{name: "origin with explicit default port", origins: []string{"https://example.com:443"}},
		{
			name:    "multiple exact origins keep order",
			origins: []string{"http://example.com", "https://app.example.com", "http://localhost:8080"},
		},
		{name: "empty list is allowed", origins: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validated, err := validateCORSAllowedOrigins(tt.origins)
			require.NoError(t, err)
			if len(tt.origins) == 0 {
				assert.Empty(t, validated)
			} else {
				assert.Equal(t, tt.origins, validated)
			}
		})
	}
}

func TestValidateCORSAllowedOrigins_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		origins []string
	}{
		{name: "empty entry", origins: []string{""}},
		{name: "whitespace only entry", origins: []string{"   "}},
		{name: "missing scheme", origins: []string{"example.com"}},
		{name: "unsupported scheme", origins: []string{"ftp://example.com"}},
		{name: "missing host", origins: []string{"http://"}},
		{name: "contains path", origins: []string{"http://example.com/path"}},
		{name: "contains trailing slash path", origins: []string{"http://example.com/"}},
		{name: "contains query string", origins: []string{"http://example.com?q=1"}},
		{name: "contains fragment", origins: []string{"http://example.com#fragment"}},
		{name: "bare wildcard", origins: []string{"*"}},
		{name: "wildcard subdomain", origins: []string{"http://*.example.com"}},
		{name: "wildcard in host", origins: []string{"https://example.*"}},
		{name: "contains whitespace", origins: []string{"http://example.com "}},
		{name: "contains userinfo", origins: []string{"http://user:pass@example.com"}},
		{name: "non canonical uppercase scheme", origins: []string{"HTTP://example.com"}},
		{
			name:    "duplicate entries",
			origins: []string{"http://example.com", "https://other.example", "http://example.com"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateCORSAllowedOrigins(tt.origins)
			require.Error(t, err)
		})
	}
}

func TestNewEngine_RejectsInvalidCORSAllowedOrigins(t *testing.T) {
	_, err := NewEngine("internal/engine/testdata/config_minimal.yaml", "internal/engine/testdata/test_theme.yaml",
		"", false, false, []string{"http://example.com/path"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid CORS allowed origin")
}

func TestCORSWhitelist_ExactMatchActualRequest(t *testing.T) {
	router := newCORSTestRouter(t, false, []string{testAllowedOrigin, testAllowedOriginWithPort})

	recorder := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, testCORSPath, nil)
	require.NoError(t, err)
	req.Header.Set(HeaderOrigin, testAllowedOrigin)

	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, testAllowedOrigin, recorder.Header().Get(HeaderAccessControlAllowOrigin))
	assert.Contains(t, recorder.Header().Values(HeaderVary), HeaderOrigin)
	assert.Equal(t, "Content-Crs, Link", recorder.Header().Get(HeaderAccessControlExposeHeaders))
}

func TestCORSWhitelist_PortDifferenceIsNotMatched(t *testing.T) {
	router := newCORSTestRouter(t, false, []string{testAllowedOriginWithPort})

	recorder := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, testCORSPath, nil)
	require.NoError(t, err)
	req.Header.Set(HeaderOrigin, testOtherPortOrigin)

	router.ServeHTTP(recorder, req)

	// actual requests from unknown origins are handled normally, but receive no allow headers
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Empty(t, recorder.Header().Get(HeaderAccessControlAllowOrigin))
	assert.Contains(t, recorder.Header().Values(HeaderVary), HeaderOrigin)
}

func TestCORSWhitelist_DeniedActualRequestHasNoAllowHeaders(t *testing.T) {
	router := newCORSTestRouter(t, false, []string{testAllowedOrigin})

	recorder := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, testCORSPath, nil)
	require.NoError(t, err)
	req.Header.Set(HeaderOrigin, testDisallowedOrigin)

	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Empty(t, recorder.Header().Get(HeaderAccessControlAllowOrigin))
	assert.Empty(t, recorder.Header().Get(HeaderAccessControlExposeHeaders))
}

func TestCORSWhitelist_RequestWithoutOriginPassesThrough(t *testing.T) {
	router := newCORSTestRouter(t, false, []string{testAllowedOrigin})

	recorder := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, testCORSPath, nil)
	require.NoError(t, err)

	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Empty(t, recorder.Header().Get(HeaderAccessControlAllowOrigin))
	assert.NotContains(t, recorder.Header().Values(HeaderVary), HeaderOrigin)
}

func TestCORSWhitelist_HeadRequest(t *testing.T) {
	router := newCORSTestRouter(t, false, []string{testAllowedOrigin})

	recorder := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodHead, testCORSPath, nil)
	require.NoError(t, err)
	req.Header.Set(HeaderOrigin, testAllowedOrigin)

	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, testAllowedOrigin, recorder.Header().Get(HeaderAccessControlAllowOrigin))
	assert.Contains(t, recorder.Header().Values(HeaderVary), HeaderOrigin)
}

func TestCORSWhitelist_AllowedPreflight(t *testing.T) {
	tests := []struct {
		name             string
		requestMethod    string
		requestHeaders   string
		wantAllowHeaders string
	}{
		{name: "GET preflight", requestMethod: http.MethodGet},
		{name: "HEAD preflight", requestMethod: http.MethodHead},
		{name: "GET preflight with allowed header", requestMethod: http.MethodGet,
			requestHeaders:   HeaderRequestedWith,
			wantAllowHeaders: HeaderRequestedWith},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := newCORSTestRouter(t, false, []string{testAllowedOrigin})

			recorder := httptest.NewRecorder()
			req, err := http.NewRequest(http.MethodOptions, testCORSPath, nil)
			require.NoError(t, err)
			req.Header.Set(HeaderOrigin, testAllowedOrigin)
			req.Header.Set(HeaderAccessControlRequestMethod, tt.requestMethod)
			if tt.requestHeaders != "" {
				req.Header.Set(HeaderAccessControlRequestHeaders, tt.requestHeaders)
			}

			router.ServeHTTP(recorder, req)

			assert.Equal(t, http.StatusNoContent, recorder.Code)
			assert.Equal(t, testAllowedOrigin, recorder.Header().Get(HeaderAccessControlAllowOrigin))
			assert.Equal(t, "GET, HEAD", recorder.Header().Get(HeaderAccessControlAllowMethods))
			assert.Equal(t, "86400", recorder.Header().Get(HeaderAccessControlMaxAge))
			assert.Equal(t, tt.wantAllowHeaders, recorder.Header().Get(HeaderAccessControlAllowHeaders))
			assert.Empty(t, recorder.Body.String())
		})
	}
}

func TestCORSWhitelist_DeniedPreflightUnknownOrigin(t *testing.T) {
	router := newCORSTestRouter(t, false, []string{testAllowedOrigin})

	recorder := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodOptions, testCORSPath, nil)
	require.NoError(t, err)
	req.Header.Set(HeaderOrigin, testDisallowedOrigin)
	req.Header.Set(HeaderAccessControlRequestMethod, http.MethodGet)

	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Equal(t, "application/problem+json", recorder.Header().Get(HeaderContentType))
	assert.Contains(t, recorder.Body.String(), "\"status\":403")
	assert.Empty(t, recorder.Header().Get(HeaderAccessControlAllowOrigin))
}

func TestCORSWhitelist_DeniedPreflightUnsupportedMethod(t *testing.T) {
	router := newCORSTestRouter(t, false, []string{testAllowedOrigin})

	recorder := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodOptions, testCORSPath, nil)
	require.NoError(t, err)
	req.Header.Set(HeaderOrigin, testAllowedOrigin)
	req.Header.Set(HeaderAccessControlRequestMethod, http.MethodPost)

	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Equal(t, "application/problem+json", recorder.Header().Get(HeaderContentType))
	assert.Contains(t, recorder.Body.String(), "\"status\":403")
	assert.Empty(t, recorder.Header().Get(HeaderAccessControlAllowOrigin))
	assert.Empty(t, recorder.Header().Get(HeaderAccessControlAllowMethods))
}

func TestCORSWhitelist_DeniedPreflightUnsupportedHeader(t *testing.T) {
	router := newCORSTestRouter(t, false, []string{testAllowedOrigin})

	recorder := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodOptions, testCORSPath, nil)
	require.NoError(t, err)
	req.Header.Set(HeaderOrigin, testAllowedOrigin)
	req.Header.Set(HeaderAccessControlRequestMethod, http.MethodGet)
	req.Header.Set(HeaderAccessControlRequestHeaders, "X-Evil-Header")

	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "\"status\":403")
	assert.Empty(t, recorder.Header().Get(HeaderAccessControlAllowOrigin))
	assert.Empty(t, recorder.Header().Get(HeaderAccessControlAllowHeaders))
}

func TestCORSWhitelist_TakesPrecedenceOverBooleanFlag(t *testing.T) {
	// even with enable-cors switched off, a configured whitelist must be active
	router := newCORSTestRouter(t, false, []string{testAllowedOrigin})

	recorder := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, testCORSPath, nil)
	require.NoError(t, err)
	req.Header.Set(HeaderOrigin, testAllowedOrigin)

	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, testAllowedOrigin, recorder.Header().Get(HeaderAccessControlAllowOrigin))
}

func TestCORSWildcard_BackwardCompatibleBooleanFlag(t *testing.T) {
	router := newCORSTestRouter(t, true, nil)

	t.Run("actual request from any origin receives wildcard", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		req, err := http.NewRequest(http.MethodGet, testCORSPath, nil)
		require.NoError(t, err)
		req.Header.Set(HeaderOrigin, testDisallowedOrigin)

		router.ServeHTTP(recorder, req)

		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Equal(t, "*", recorder.Header().Get(HeaderAccessControlAllowOrigin))
	})

	t.Run("preflight keeps existing 200 wildcard behavior", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		req, err := http.NewRequest(http.MethodOptions, testCORSPath, nil)
		require.NoError(t, err)
		req.Header.Set(HeaderOrigin, testDisallowedOrigin)
		req.Header.Set(HeaderAccessControlRequestMethod, http.MethodGet)

		router.ServeHTTP(recorder, req)

		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Equal(t, "*", recorder.Header().Get(HeaderAccessControlAllowOrigin))
	})

	t.Run("request without origin receives no CORS headers", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		req, err := http.NewRequest(http.MethodGet, testCORSPath, nil)
		require.NoError(t, err)

		router.ServeHTTP(recorder, req)

		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Empty(t, recorder.Header().Get(HeaderAccessControlAllowOrigin))
	})
}

func TestCORSDisabled_NoCORSHeaders(t *testing.T) {
	router := newCORSTestRouter(t, false, nil)

	recorder := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, testCORSPath, nil)
	require.NoError(t, err)
	req.Header.Set(HeaderOrigin, testAllowedOrigin)

	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Empty(t, recorder.Header().Get(HeaderAccessControlAllowOrigin))
}
