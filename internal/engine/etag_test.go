package engine

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"

	"github.com/PDOK/gokoala/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/language"
)

var strongETagPattern = regexp.MustCompile(`^"[a-f0-9]{64}"$`)

func newConditionalTestEngine() *Engine {
	cfg := &config.Config{
		BaseURL:            config.URL{URL: &url.URL{Scheme: "https", Host: "api.foobar.example", Path: "/"}},
		AvailableLanguages: []config.Language{{Tag: language.Dutch}, {Tag: language.English}},
	}

	return &Engine{
		Config:    cfg,
		OpenAPI:   newOpenAPI(cfg, []string{""}, nil),
		Templates: &Templates{RenderedTemplates: map[TemplateKey][]byte{}},
		CN:        newContentNegotiation(cfg.AvailableLanguages),
	}
}

func serveWithRecorder(t *testing.T, e *Engine, method string, target string, opts ...ServeOption) *httptest.ResponseRecorder {
	t.Helper()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, target, nil)
	e.Serve(w, r, opts...)

	return w
}

func assertStrongETag(t *testing.T, headers http.Header) string {
	t.Helper()

	etag := headers.Get(HeaderETag)
	assert.Regexp(t, strongETagPattern, etag)

	return etag
}

func assertVaryDeclared(t *testing.T, headers http.Header, expected ...string) {
	t.Helper()

	for _, header := range expected {
		assert.True(t, varyContains(headers.Values(HeaderVary), header),
			"expected Vary to contain %q, got %v", header, headers.Values(HeaderVary))
	}
}

func TestGenerateETag_StableAndVariantSpecific(t *testing.T) {
	representation := []byte(`{"foo":"bar"}`)

	etag := generateETag(MediaTypeJSON, "variant-a", representation)
	assert.Regexp(t, strongETagPattern, etag)

	// identical inputs always produce the same validator
	assert.Equal(t, etag, generateETag(MediaTypeJSON, "variant-a", representation))

	// the representation bytes participate in the validator
	assert.NotEqual(t, etag, generateETag(MediaTypeJSON, "variant-a", []byte(`{"foo":"baz"}`)))

	// the negotiated media type participates in the validator
	assert.NotEqual(t, etag, generateETag(MediaTypeHTML, "variant-a", representation))

	// the response variant participates in the validator, even with identical bytes
	assert.NotEqual(t, etag, generateETag(MediaTypeJSON, "variant-b", representation))
}

func TestSplitEntityTags(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected []string
	}{
		{"single", `"a"`, []string{`"a"`}},
		{"multiple with whitespace", ` "a" , W/"b" ,"c" `, []string{`"a"`, `W/"b"`, `"c"`}},
		{"comma inside quoted string", `"a,b"`, []string{`"a,b"`}},
		{"weak tag containing comma", `W/"a,b", "c"`, []string{`W/"a,b"`, `"c"`}},
		{"wildcard", `*`, []string{`*`}},
		{"empty separators", " , ,", []string{}},
		{"empty", "", []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, splitEntityTags(tt.header))
		})
	}
}

func TestMatchesIfNoneMatch(t *testing.T) {
	tests := []struct {
		name        string
		header      string
		currentETag string
		expected    bool
	}{
		{"no header", "", `"a"`, false},
		{"exact strong match", `"a"`, `"a"`, true},
		{"weak request tag matches strong current", `W/"a"`, `"a"`, true},
		{"strong request tag matches weak current", `"a"`, `W/"a"`, true},
		{"two weak tags match", `W/"a"`, `W/"a"`, true},
		{"match in list of values", `"b", "c", "a"`, `"a"`, true},
		{"match in list with whitespace", `"b" ,  "a"`, `"a"`, true},
		{"no match in list", `"b","c"`, `"a"`, false},
		{"wildcard matches everything", `*`, `"a"`, true},
		{"wildcard with surrounding whitespace", `  *  `, `"a"`, true},
		{"comma inside opaque tag matches", `"a,b"`, `"a,b"`, true},
		{"no prefix matching", `"ab"`, `"a"`, false},
		{"weak non-matching tag", `W/"other"`, `"a"`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, matchesIfNoneMatch(tt.header, tt.currentETag))
		})
	}
}

func TestServe_DynamicJSON_ETag(t *testing.T) {
	e := newConditionalTestEngine()

	jsonOpts := func(body map[string]string) []ServeOption {
		return []ServeOption{
			ServeValidation(false, false),
			ServeJSON(body),
			ServeContentType(MediaTypeJSON),
		}
	}

	// successful response carries a stable strong validator and declares Vary
	first := serveWithRecorder(t, e, http.MethodGet, "/items", jsonOpts(map[string]string{"foo": "bar"})...)
	require.Equal(t, http.StatusOK, first.Code)
	assert.JSONEq(t, `{"foo":"bar"}`, first.Body.String())
	etag := assertStrongETag(t, first.Header())
	assertVaryDeclared(t, first.Header(), HeaderAccept, HeaderAcceptLanguage, HeaderCookie)

	// repeating the same request yields the same validator
	second := serveWithRecorder(t, e, http.MethodGet, "/items", jsonOpts(map[string]string{"foo": "bar"})...)
	assert.Equal(t, http.StatusOK, second.Code)
	assert.Equal(t, etag, second.Header().Get(HeaderETag))

	// a different representation gets a different validator
	other := serveWithRecorder(t, e, http.MethodGet, "/items", jsonOpts(map[string]string{"foo": "baz"})...)
	assert.NotEqual(t, etag, other.Header().Get(HeaderETag))
}

func TestServe_DynamicJSON_ValidationShortcutAlsoBuffered(t *testing.T) {
	// the former streaming shortcut (response validation disabled) must be buffered
	// so dynamic JSON responses receive validators as well
	e := newConditionalTestEngine()

	w := serveWithRecorder(t, e, http.MethodGet, "/items",
		ServeValidation(false, false),
		ServeJSON(map[string]string{"foo": "bar"}),
		ServeContentType(MediaTypeGeoJSON))

	require.Equal(t, http.StatusOK, w.Code)
	assertStrongETag(t, w.Header())
	actualContentType := w.Header().Get(HeaderContentType)
	assert.Equal(t, "application/geo+json", actualContentType)
}

func TestServe_PreRenderedTemplate_ETag(t *testing.T) {
	e := newConditionalTestEngine()

	key := TemplateKey{Name: "landing", Format: FormatJSON, Language: language.Dutch}
	e.Templates.RenderedTemplates[key] = []byte(`{"template":"rendered"}`)

	first := serveWithRecorder(t, e, http.MethodGet, "/", ServeValidation(false, false), ServeTemplate(key))
	require.Equal(t, http.StatusOK, first.Code)
	assert.JSONEq(t, `{"template":"rendered"}`, first.Body.String())
	etag := assertStrongETag(t, first.Header())

	// pre-rendered templates are immutable, so the validator stays stable across requests
	second := serveWithRecorder(t, e, http.MethodGet, "/", ServeValidation(false, false), ServeTemplate(key))
	assert.Equal(t, etag, second.Header().Get(HeaderETag))
}

func TestServe_LanguageVariants_DistinctETags(t *testing.T) {
	e := newConditionalTestEngine()

	nlKey := TemplateKey{Name: "page", Format: FormatJSON, Language: language.Dutch}
	enKey := TemplateKey{Name: "page", Format: FormatJSON, Language: language.English}

	// deliberately identical bytes: the variant (language) itself must change the validator
	identicalRepresentation := []byte(`{"same":"bytes"}`)
	e.Templates.RenderedTemplates[nlKey] = identicalRepresentation
	e.Templates.RenderedTemplates[enKey] = identicalRepresentation

	nl := serveWithRecorder(t, e, http.MethodGet, "/page", ServeValidation(false, false), ServeTemplate(nlKey))
	en := serveWithRecorder(t, e, http.MethodGet, "/page", ServeValidation(false, false), ServeTemplate(enKey))

	require.Equal(t, http.StatusOK, nl.Code)
	require.Equal(t, http.StatusOK, en.Code)
	assert.NotEqual(t, nl.Header().Get(HeaderETag), en.Header().Get(HeaderETag))

	// each language validator is stable
	nlRepeat := serveWithRecorder(t, e, http.MethodGet, "/page", ServeValidation(false, false), ServeTemplate(nlKey))
	assert.Equal(t, nl.Header().Get(HeaderETag), nlRepeat.Header().Get(HeaderETag))
}

func TestServe_FormatAndMediaTypeVariants_DistinctETags(t *testing.T) {
	e := newConditionalTestEngine()

	jsonKey := TemplateKey{Name: "page", Format: FormatJSON, Language: language.Dutch}
	htmlKey := TemplateKey{Name: "page", Format: FormatHTML, Language: language.Dutch}
	e.Templates.RenderedTemplates[jsonKey] = []byte(`{}`)
	e.Templates.RenderedTemplates[htmlKey] = []byte(`<html></html>`)

	jsonResp := serveWithRecorder(t, e, http.MethodGet, "/page", ServeValidation(false, false), ServeTemplate(jsonKey))
	htmlResp := serveWithRecorder(t, e, http.MethodGet, "/page", ServeValidation(false, false), ServeTemplate(htmlKey))

	assert.NotEqual(t, jsonResp.Header().Get(HeaderETag), htmlResp.Header().Get(HeaderETag))
	assert.Equal(t, "application/json", jsonResp.Header().Get(HeaderContentType))
	assert.Equal(t, MediaTypeHTML, htmlResp.Header().Get(HeaderContentType))

	// identical bytes but a different negotiated media type must not share a validator
	representation := []byte("same-bytes")
	first := serveWithRecorder(t, e, http.MethodGet, "/raw",
		ServeValidation(false, false), ServePreRenderedOutput(representation), ServeContentType(MediaTypeJSON))
	second := serveWithRecorder(t, e, http.MethodGet, "/raw",
		ServeValidation(false, false), ServePreRenderedOutput(representation), ServeContentType(MediaTypeHTML))
	assert.NotEqual(t, first.Header().Get(HeaderETag), second.Header().Get(HeaderETag))
}

func TestServe_URLVariants_DistinctETags(t *testing.T) {
	e := newConditionalTestEngine()

	representation := []byte(`{"identical":true}`)
	first := serveWithRecorder(t, e, http.MethodGet, "/collections/a/items",
		ServeValidation(false, false), ServePreRenderedOutput(representation), ServeContentType(MediaTypeJSON))
	second := serveWithRecorder(t, e, http.MethodGet, "/collections/b/items",
		ServeValidation(false, false), ServePreRenderedOutput(representation), ServeContentType(MediaTypeJSON))

	assert.NotEqual(t, first.Header().Get(HeaderETag), second.Header().Get(HeaderETag))

	// query string variants are distinct resources as well
	pageOne := serveWithRecorder(t, e, http.MethodGet, "/items?limit=10&offset=0",
		ServeValidation(false, false), ServePreRenderedOutput(representation), ServeContentType(MediaTypeJSON))
	pageTwo := serveWithRecorder(t, e, http.MethodGet, "/items?limit=10&offset=10",
		ServeValidation(false, false), ServePreRenderedOutput(representation), ServeContentType(MediaTypeJSON))
	assert.NotEqual(t, pageOne.Header().Get(HeaderETag), pageTwo.Header().Get(HeaderETag))
}

func TestServe_IfNoneMatch_ConditionalResponses(t *testing.T) {
	e := newConditionalTestEngine()

	opts := []ServeOption{
		ServeValidation(false, false),
		ServeJSON(map[string]string{"foo": "bar"}),
		ServeContentType(MediaTypeJSON),
	}
	currentETag := serveWithRecorder(t, e, http.MethodGet, "/items", opts...).Header().Get(HeaderETag)
	require.Regexp(t, strongETagPattern, currentETag)

	tests := []struct {
		name           string
		ifNoneMatch    string
		expectedStatus int
		expectBody     bool
	}{
		{"exact validator returns 304", currentETag, http.StatusNotModified, false},
		{"weak validator returns 304", "W/" + currentETag, http.StatusNotModified, false},
		{"matching value in list returns 304", `"other", ` + currentETag, http.StatusNotModified, false},
		{"wildcard returns 304", "*", http.StatusNotModified, false},
		{"non-matching validator returns 200", `"other"`, http.StatusOK, true},
		{"weak non-matching validator returns 200", `W/"other"`, http.StatusOK, true},
		{"no validator returns 200", "", http.StatusOK, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/items", nil)
			if tt.ifNoneMatch != "" {
				r.Header.Set(HeaderIfNoneMatch, tt.ifNoneMatch)
			}

			e.Serve(w, r, opts...)

			assert.Equal(t, tt.expectedStatus, w.Code)
			// the current validator is advertised on both 200 and 304 responses
			assert.Equal(t, currentETag, w.Header().Get(HeaderETag))
			assertVaryDeclared(t, w.Header(), HeaderAccept, HeaderAcceptLanguage, HeaderCookie)
			if tt.expectBody {
				assert.JSONEq(t, `{"foo":"bar"}`, w.Body.String())
			} else {
				assert.Empty(t, w.Body.Bytes(), "304 response must not contain a body")
			}
		})
	}
}

func TestServe_HEAD_SameValidatorAsGET(t *testing.T) {
	e := newConditionalTestEngine()

	opts := []ServeOption{
		ServeValidation(false, false),
		ServeJSON(map[string]string{"foo": "bar"}),
		ServeContentType(MediaTypeJSON),
	}

	getResp := serveWithRecorder(t, e, http.MethodGet, "/items", opts...)
	headResp := serveWithRecorder(t, e, http.MethodHead, "/items", opts...)

	assert.Equal(t, http.StatusOK, headResp.Code)
	assert.Equal(t, getResp.Header().Get(HeaderETag), headResp.Header().Get(HeaderETag))

	// conditional HEAD also yields a body-less 304 with the same validator
	conditionalHead := httptest.NewRecorder()
	headReq := httptest.NewRequest(http.MethodHead, "/items", nil)
	headReq.Header.Set(HeaderIfNoneMatch, getResp.Header().Get(HeaderETag))
	e.Serve(conditionalHead, headReq, opts...)

	assert.Equal(t, http.StatusNotModified, conditionalHead.Code)
	assert.Empty(t, conditionalHead.Body.Bytes())
	assert.Equal(t, getResp.Header().Get(HeaderETag), conditionalHead.Header().Get(HeaderETag))
}

func TestServe_DoesNotOverwriteExistingETag(t *testing.T) {
	e := newConditionalTestEngine()

	opts := []ServeOption{
		ServeValidation(false, false),
		ServePreRenderedOutput([]byte(`{"foo":"bar"}`)),
		ServeContentType(MediaTypeJSON),
	}

	// an ETag already set on the response (e.g. by the reverse proxy) must be preserved
	w := httptest.NewRecorder()
	w.Header().Set(HeaderETag, `"upstream-validator"`)
	r := httptest.NewRequest(http.MethodGet, "/proxied", nil)
	e.Serve(w, r, opts...)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, `"upstream-validator"`, w.Header().Get(HeaderETag))
	assert.NotRegexp(t, strongETagPattern, w.Header().Get(HeaderETag))

	// conditional requests still work against the preserved (upstream) validator
	conditional := httptest.NewRecorder()
	conditional.Header().Set(HeaderETag, `"upstream-validator"`)
	conditionalReq := httptest.NewRequest(http.MethodGet, "/proxied", nil)
	conditionalReq.Header.Set(HeaderIfNoneMatch, `"upstream-validator"`)
	e.Serve(conditional, conditionalReq, opts...)

	assert.Equal(t, http.StatusNotModified, conditional.Code)
	assert.Empty(t, conditional.Body.Bytes())
	assert.Equal(t, `"upstream-validator"`, conditional.Header().Get(HeaderETag))
}

func TestEngine_ConditionalGet_WithOpenAPIValidation(t *testing.T) {
	// conditional headers must survive the default OpenAPI request/response validation
	engine, err := NewEngine("internal/engine/testdata/config_minimal.yaml",
		"internal/engine/testdata/test_theme.yaml", "", false, true)
	require.NoError(t, err)

	templateKey := NewTemplateKey("internal/ogc/common/core/templates/landing-page.go.json")
	engine.RenderTemplates("/", nil, templateKey)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		engine.Serve(w, r, ServeTemplate(templateKey)) // request and response validation enabled
	})

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil))
	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get(HeaderETag)
	require.NotEmpty(t, etag)

	conditional := httptest.NewRecorder()
	conditionalReq := httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil)
	conditionalReq.Header.Set(HeaderIfNoneMatch, etag)
	handler.ServeHTTP(conditional, conditionalReq)

	assert.Equal(t, http.StatusNotModified, conditional.Code)
	assert.Empty(t, conditional.Body.Bytes())
	assert.Equal(t, etag, conditional.Header().Get(HeaderETag))
}

func TestEngine_ConditionalGet_RouterIntegration(t *testing.T) {
	engine, err := NewEngine("internal/engine/testdata/config_etag.yaml",
		"internal/engine/testdata/test_theme.yaml", "", false, true)
	require.NoError(t, err)

	pageKey := func(lang language.Tag) TemplateKey {
		return TemplateKey{Name: "page", Format: FormatJSON, Language: lang}
	}
	engine.Templates.RenderedTemplates[pageKey(language.Dutch)] = []byte(`{"language":"nl"}`)
	engine.Templates.RenderedTemplates[pageKey(language.English)] = []byte(`{"language":"en"}`)

	engine.Router.Get("/page", func(w http.ResponseWriter, r *http.Request) {
		key := pageKey(engine.CN.NegotiateLanguage(w, r))
		engine.Serve(w, r, ServeTemplate(key), ServeValidation(false, false))
	})

	server := httptest.NewServer(engine.Router)
	defer server.Close()

	client := server.Client()

	// language negotiation through Accept-Language produces distinct validators
	nlResp := doRequest(t, client, server.URL+"/page", map[string]string{
		HeaderAcceptLanguage: "nl",
	})
	enResp := doRequest(t, client, server.URL+"/page", map[string]string{
		HeaderAcceptLanguage: "en",
	})
	require.Equal(t, http.StatusOK, nlResp.StatusCode)
	require.Equal(t, http.StatusOK, enResp.StatusCode)
	nlETag := nlResp.Header.Get(HeaderETag)
	enETag := enResp.Header.Get(HeaderETag)
	assert.Regexp(t, strongETagPattern, nlETag)
	assert.NotEqual(t, nlETag, enETag)
	assertVaryDeclared(t, nlResp.Header, HeaderAcceptLanguage)

	// conditional GET with the current validator returns 304 without a body
	notModified := doRequest(t, client, server.URL+"/page", map[string]string{
		HeaderAcceptLanguage: "nl",
		HeaderIfNoneMatch:    nlETag,
	})
	assert.Equal(t, http.StatusNotModified, notModified.StatusCode)
	assert.Equal(t, nlETag, notModified.Header.Get(HeaderETag))
	body, err := io.ReadAll(notModified.Body)
	require.NoError(t, err)
	assert.Empty(t, body)
	require.NoError(t, notModified.Body.Close())

	// a stale validator returns the full 200 representation
	stale := doRequest(t, client, server.URL+"/page", map[string]string{
		HeaderAcceptLanguage: "nl",
		HeaderIfNoneMatch:    `"stale"`,
	})
	assert.Equal(t, http.StatusOK, stale.StatusCode)
	require.NoError(t, stale.Body.Close())

	// HEAD exposes the same validator as GET and never a body
	headResp, err := client.Head(server.URL + "/page")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, headResp.StatusCode)
	assert.Equal(t, nlETag, headResp.Header.Get(HeaderETag))
	headBody, err := io.ReadAll(headResp.Body)
	require.NoError(t, err)
	assert.Empty(t, headBody)
	require.NoError(t, headResp.Body.Close())

	// compression keeps the same validator (of the uncompressed representation)
	// and the compression middleware adds Vary: Accept-Encoding
	gzipClient := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	gzipResp := doRequest(t, gzipClient, server.URL+"/page", map[string]string{
		"Accept-Encoding": "gzip",
	})
	require.Equal(t, http.StatusOK, gzipResp.StatusCode)
	assert.Equal(t, FormatGzip, gzipResp.Header.Get(HeaderContentEncoding))
	assert.Equal(t, nlETag, gzipResp.Header.Get(HeaderETag))
	assertVaryDeclared(t, gzipResp.Header, "Accept-Encoding")
	reader, err := gzip.NewReader(gzipResp.Body)
	require.NoError(t, err)
	uncompressed, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.JSONEq(t, `{"language":"nl"}`, string(uncompressed))
	require.NoError(t, gzipResp.Body.Close())
}

func doRequest(t *testing.T, client *http.Client, target string, headers map[string]string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, target, nil)
	require.NoError(t, err)
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(req)
	require.NoError(t, err)

	return resp
}

func TestReverseProxy_ETagHandling(t *testing.T) {
	t.Run("upstream ETag is preserved on validated responses", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(HeaderETag, `"upstream-validator"`)
			w.Header().Set(HeaderContentType, MediaTypeJSON)
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
				t.Fatal(err)
			}
		}))
		defer upstream.Close()

		engine, targetURL := makeEngine(upstream)
		rec, req := makeAPICall(t, upstream.URL)

		engine.ReverseProxyAndValidate(rec, req, targetURL, false, MediaTypeJSON, true)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, `"upstream-validator"`, rec.Header().Get(HeaderETag))
		assert.NotRegexp(t, strongETagPattern, rec.Header().Get(HeaderETag))
		assert.JSONEq(t, `{"ok":true}`, rec.Body.String())
	})

	t.Run("conditional request with upstream ETag returns 304", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(HeaderETag, `"upstream-validator"`)
			w.Header().Set(HeaderContentType, MediaTypeJSON)
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
				t.Fatal(err)
			}
		}))
		defer upstream.Close()

		engine, targetURL := makeEngine(upstream)
		rec, req := makeAPICall(t, upstream.URL)
		req.Header.Set(HeaderIfNoneMatch, `"upstream-validator"`)

		engine.ReverseProxyAndValidate(rec, req, targetURL, false, MediaTypeJSON, true)

		assert.Equal(t, http.StatusNotModified, rec.Code)
		assert.Empty(t, rec.Body.Bytes())
		assert.Equal(t, `"upstream-validator"`, rec.Header().Get(HeaderETag))
	})

	t.Run("ETag generated when upstream provides none", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(HeaderContentType, MediaTypeJSON)
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
				t.Fatal(err)
			}
		}))
		defer upstream.Close()

		engine, targetURL := makeEngine(upstream)
		rec, req := makeAPICall(t, upstream.URL)

		engine.ReverseProxyAndValidate(rec, req, targetURL, false, MediaTypeJSON, true)

		assert.Equal(t, http.StatusOK, rec.Code)
		assertStrongETag(t, rec.Header())
	})

	t.Run("upstream ETag passes through plain reverse proxy untouched", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(HeaderETag, `"upstream-validator"`)
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte("mock")); err != nil {
				t.Fatal(err)
			}
		}))
		defer upstream.Close()

		engine, targetURL := makeEngine(upstream)
		rec, req := makeAPICall(t, upstream.URL)

		engine.ReverseProxy(rec, req, targetURL, false, "")

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, `"upstream-validator"`, rec.Header().Get(HeaderETag))
		assert.NotRegexp(t, strongETagPattern, rec.Header().Get(HeaderETag))
	})
}
