package features

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/PDOK/gokoala/internal/engine"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	schemasAllAvailableConfig = "internal/ogc/features/testdata/geopackage/config_features_bag_multiple_feature_tables.yaml"
	schemasBagConfig          = "internal/ogc/features/testdata/geopackage/config_features_bag.yaml"
	schemasFailureConfig      = "internal/ogc/features/testdata/geopackage/config_features_schemas_failure.yaml"
	schemasGoldenFile         = "internal/ogc/features/testdata/expected_schemas.json"
)

type catalogResponse struct {
	Schema      string `json:"$schema"`
	ID          string `json:"$id"`
	Type        string `json:"type"`
	Version     string `json:"version"`
	Collections []struct {
		ID            string          `json:"id"`
		Href          string          `json:"href"`
		Status        string          `json:"status"`
		ErrorCategory string          `json:"errorCategory"`
		Schema        json.RawMessage `json:"schema"`
	} `json:"collections"`
}

func newSchemasRequest(t *testing.T, target string, accept string) *http.Request {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, target, nil)
	require.NoError(t, err)
	if accept != "" {
		req.Header.Set(engine.HeaderAccept, accept)
	}
	rctx := chi.NewRouteContext()
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	return req
}

func TestSchemasCatalogMultiCollectionOrderAndStableJSON(t *testing.T) {
	t.Parallel()

	newEngine, err := engine.NewEngine(schemasAllAvailableConfig, "internal/engine/testdata/test_theme.yaml", "", false, true)
	require.NoError(t, err)
	f := NewFeatures(newEngine)

	// when: request the aggregated schema catalog twice
	first := httptest.NewRecorder()
	f.Schemas().ServeHTTP(first, newSchemasRequest(t, "http://localhost:8080/schemas?f=json", ""))
	second := httptest.NewRecorder()
	f.Schemas().ServeHTTP(second, newSchemasRequest(t, "http://localhost:8080/schemas?f=json", ""))

	// then: OK with the JSON Schema mediatype
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	assert.Equal(t, engine.MediaTypeJSONSchema, first.Header().Get(engine.HeaderContentType))

	var catalog catalogResponse
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &catalog))

	// and: envelope carries a stable version/generation identifier
	assert.Equal(t, "https://json-schema.org/draft/2020-12/schema", catalog.Schema)
	assert.Equal(t, "http://localhost:8080/schemas", catalog.ID)
	assert.Equal(t, "object", catalog.Type)
	assert.Equal(t, "1.0.2", catalog.Version)

	// and: collections are returned in configuration order, all available with a schema
	require.Len(t, catalog.Collections, 2)
	assert.Equal(t, []string{"ligplaatsen", "standplaatsen"}, []string{
		catalog.Collections[0].ID, catalog.Collections[1].ID,
	})
	for index, entry := range catalog.Collections {
		assert.Equal(t, schemaStatusAvailable, entry.Status)
		assert.Empty(t, entry.ErrorCategory)
		assert.NotEmpty(t, entry.Schema)
		assert.NotEqual(t, "null", string(entry.Schema))
		assert.Equal(t, "http://localhost:8080/collections/"+entry.ID+"/schema", entry.Href,
			"entry %d should carry a stable href", index)
	}

	// and: the full document matches the golden file
	expectedBody, err := os.ReadFile(schemasGoldenFile)
	require.NoError(t, err)
	assert.JSONEq(t, string(expectedBody), first.Body.String())

	// and: repeated requests on the same baseline return byte-identical (stable) JSON
	assert.Equal(t, first.Body.String(), second.Body.String())
}

func TestSchemasCatalogKeepsFailedCollectionAmongAvailableOnes(t *testing.T) {
	t.Parallel()

	// the bag config defines three collections in order; "baz" has no backing table in the datasource
	newEngine, err := engine.NewEngine(schemasBagConfig, "internal/engine/testdata/test_theme.yaml", "", false, true)
	require.NoError(t, err)
	f := NewFeatures(newEngine)

	recorder := httptest.NewRecorder()
	f.Schemas().ServeHTTP(recorder, newSchemasRequest(t, "http://localhost:8080/schemas?f=json", ""))

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	var catalog catalogResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &catalog))

	// then: configuration order is preserved and the failing collection is not omitted
	require.Len(t, catalog.Collections, 3)
	assert.Equal(t, []string{"foo", "bar", "baz"}, []string{
		catalog.Collections[0].ID, catalog.Collections[1].ID, catalog.Collections[2].ID,
	})
	assert.Equal(t, schemaStatusAvailable, catalog.Collections[0].Status)
	assert.Equal(t, schemaStatusAvailable, catalog.Collections[1].Status)

	assert.Equal(t, schemaStatusFailed, catalog.Collections[2].Status)
	assert.Equal(t, schemaErrorCategoryDatasource, catalog.Collections[2].ErrorCategory)
	assert.JSONEq(t, "null", string(catalog.Collections[2].Schema))
	assert.Equal(t, "http://localhost:8080/collections/baz/schema", catalog.Collections[2].Href)
}

func TestSchemasCatalogEmbedsSingleCollectionSchema(t *testing.T) {
	t.Parallel()

	newEngine, err := engine.NewEngine(schemasAllAvailableConfig, "internal/engine/testdata/test_theme.yaml", "", false, true)
	require.NoError(t, err)
	f := NewFeatures(newEngine)

	// when: requesting both the aggregated catalog and a single collection schema
	catalogRecorder := httptest.NewRecorder()
	f.Schemas().ServeHTTP(catalogRecorder, newSchemasRequest(t, "http://localhost:8080/schemas?f=json", ""))

	singleRecorder := httptest.NewRecorder()
	singleRequest, err := createRequest("http://localhost:8080/collections/:collectionId/schema", "ligplaatsen", "", "json")
	require.NoError(t, err)
	f.Schema().ServeHTTP(singleRecorder, singleRequest)

	require.Equal(t, http.StatusOK, catalogRecorder.Code)
	require.Equal(t, http.StatusOK, singleRecorder.Code)

	var catalog catalogResponse
	require.NoError(t, json.Unmarshal(catalogRecorder.Body.Bytes(), &catalog))
	require.NotEmpty(t, catalog.Collections)

	// then: the embedded schema is exactly the single collection schema (same field type rules)
	assert.JSONEq(t, singleRecorder.Body.String(), string(catalog.Collections[0].Schema))
	assert.Equal(t, engine.MediaTypeJSONSchema, singleRecorder.Header().Get(engine.HeaderContentType))
}

func TestSchemasCatalogSingleCollectionFailure(t *testing.T) {
	t.Parallel()

	newEngine, err := engine.NewEngine(schemasFailureConfig, "internal/engine/testdata/test_theme.yaml", "", false, true)
	require.NoError(t, err)
	f := NewFeatures(newEngine)

	recorder := httptest.NewRecorder()
	f.Schemas().ServeHTTP(recorder, newSchemasRequest(t, "http://localhost:8080/schemas?f=json", ""))

	// then: the batch as a whole still succeeds, no generic error overrides the result
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, engine.MediaTypeJSONSchema, recorder.Header().Get(engine.HeaderContentType))

	var catalog catalogResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &catalog))

	// and: both collections are present, in configuration order, with only the broken one failed
	require.Len(t, catalog.Collections, 2)
	assert.Equal(t, "foo", catalog.Collections[0].ID)
	assert.Equal(t, schemaStatusAvailable, catalog.Collections[0].Status)
	assert.NotEqual(t, "null", string(catalog.Collections[0].Schema))

	assert.Equal(t, "broken", catalog.Collections[1].ID)
	assert.Equal(t, schemaStatusFailed, catalog.Collections[1].Status)
	assert.Equal(t, schemaErrorCategoryDatasource, catalog.Collections[1].ErrorCategory)
	assert.JSONEq(t, "null", string(catalog.Collections[1].Schema))
	assert.Equal(t, "http://localhost:8080/collections/broken/schema", catalog.Collections[1].Href)
}

func TestBuildSchemasCatalogWithEmptyCollections(t *testing.T) {
	t.Parallel()

	newEngine, err := engine.NewEngine(schemasBagConfig, "internal/engine/testdata/test_theme.yaml", "", false, true)
	require.NoError(t, err)
	newEngine.Config.OgcAPI.Features.Collections = nil

	catalog := buildSchemasCatalog(newEngine, map[string]schemaFailure{})

	// then: an empty, non-nil collections array is rendered (not null)
	require.NotNil(t, catalog.Collections)
	assert.Len(t, catalog.Collections, 0)
	assert.Equal(t, "1.0.2", catalog.Version)

	raw, err := json.Marshal(catalog)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"collections":[]`)
}

func TestSchemasCatalogUnknownFormatProperty(t *testing.T) {
	t.Parallel()

	newEngine, err := engine.NewEngine(schemasAllAvailableConfig, "internal/engine/testdata/test_theme.yaml", "", false, true)
	require.NoError(t, err)
	f := NewFeatures(newEngine)

	recorder := httptest.NewRecorder()
	f.Schemas().ServeHTTP(recorder, newSchemasRequest(t, "http://localhost:8080/schemas?f=docx", ""))

	// then: unknown f value is rejected by OpenAPI request validation
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestSchemasCatalogUnsupportedFormat(t *testing.T) {
	t.Parallel()

	newEngine, err := engine.NewEngine(schemasAllAvailableConfig, "internal/engine/testdata/test_theme.yaml", "", false, true)
	require.NoError(t, err)
	f := NewFeatures(newEngine)

	recorder := httptest.NewRecorder()
	f.Schemas().ServeHTTP(recorder, newSchemasRequest(t, "http://localhost:8080/schemas?f=html", ""))

	assert.Equal(t, http.StatusNotAcceptable, recorder.Code)
}

func TestSchemasCatalogContentNegotiation(t *testing.T) {
	t.Parallel()

	newEngine, err := engine.NewEngine(schemasAllAvailableConfig, "internal/engine/testdata/test_theme.yaml", "", false, true)
	require.NoError(t, err)
	f := NewFeatures(newEngine)

	t.Run("Accept application/schema+json", func(t *testing.T) {
		t.Parallel()

		recorder := httptest.NewRecorder()
		f.Schemas().ServeHTTP(recorder,
			newSchemasRequest(t, "http://localhost:8080/schemas", engine.MediaTypeJSONSchema))

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, engine.MediaTypeJSONSchema, recorder.Header().Get(engine.HeaderContentType))
	})

	t.Run("Accept text/html is not acceptable for the JSON-only catalog", func(t *testing.T) {
		t.Parallel()

		recorder := httptest.NewRecorder()
		f.Schemas().ServeHTTP(recorder,
			newSchemasRequest(t, "http://localhost:8080/schemas", engine.MediaTypeHTML))

		assert.Equal(t, http.StatusNotAcceptable, recorder.Code)
	})
}

func TestSchemasEndpointNotRegisteredWithoutFeatures(t *testing.T) {
	t.Parallel()

	// given: an engine without OGC API Features configured
	newEngine, err := engine.NewEngine("internal/engine/testdata/config_minimal.yaml",
		"internal/engine/testdata/test_theme.yaml", "", false, true)
	require.NoError(t, err)

	// when: requesting the aggregated schemas endpoint through the router
	recorder := httptest.NewRecorder()
	newEngine.Router.ServeHTTP(recorder, newSchemasRequest(t, "http://localhost:8080/schemas?f=json", ""))

	// then: the capability is treated as unregistered (404), startup itself didn't fail
	assert.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestSchemasCatalogAdvertisedInOpenAPI(t *testing.T) {
	t.Parallel()

	newEngine, err := engine.NewEngine(schemasAllAvailableConfig, "internal/engine/testdata/test_theme.yaml", "", false, true)
	require.NoError(t, err)
	NewFeatures(newEngine)

	spec := string(newEngine.OpenAPI.SpecJSON)
	assert.Contains(t, spec, `"/schemas"`)
	assert.Contains(t, spec, `"operationId": "getSchemas"`)
	assert.Contains(t, spec, engine.MediaTypeJSONSchema)
	assert.Contains(t, spec, `"schemasCatalog"`)
	assert.Contains(t, spec, `"collectionSchemaEntry"`)
}
