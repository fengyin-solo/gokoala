package features

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/PDOK/gokoala/internal/engine"
	g "github.com/PDOK/gokoala/internal/ogc/common/geospatial"
)

const schemasCatalogPath = "/schemas"

const (
	jsonSchemaDraft202012 = "https://json-schema.org/draft/2020-12/schema"
)

// Per-collection schema statuses advertised in the aggregated schema catalog.
const (
	schemaStatusAvailable = "available"
	schemaStatusFailed    = "failed"
)

// Public, implementation-independent error categories. Never expose raw (potentially sensitive)
// datasource errors to clients, only these stable category identifiers.
const (
	schemaErrorCategoryDatasource    = "datasource-error"
	schemaErrorCategoryConfiguration = "configuration-error"
	schemaErrorCategoryInternal      = "internal-error"
)

// schemaFailure records why the schema of a single collection could not be generated.
// The error itself is only used for logging, the category is safe to expose publicly.
type schemaFailure struct {
	Category string
	Err      error
}

// collectionSchemaEntry is a single entry in the aggregated schema catalog. The JSON Schema is
// embedded verbatim from the pre-rendered single collection schema, so the same field type rules
// apply. When generation failed the schema is null and a public error category is provided.
type collectionSchemaEntry struct {
	CollectionID  string          `json:"id"`
	Href          string          `json:"href"`
	Status        string          `json:"status"`
	ErrorCategory string          `json:"errorCategory,omitempty"`
	Schema        json.RawMessage `json:"schema"`
}

// schemasCatalog is the response body of the aggregated GET /schemas endpoint. It is a JSON Schema
// document itself (media type application/schema+json) describing all configured feature collections.
type schemasCatalog struct {
	Schema      string                  `json:"$schema"`
	ID          string                  `json:"$id"`
	Type        string                  `json:"type"`
	Version     string                  `json:"version"`
	Collections []collectionSchemaEntry `json:"collections"`
}

// Schemas serves the aggregated schema catalog: the JSON Schemas of all configured feature
// collections in a single response, in the same order as the collections are configured.
// Only JSON (application/schema+json) is offered, other formats result in a 406.
func (f *Features) Schemas() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := f.engine.OpenAPI.ValidateRequest(r); err != nil {
			engine.RenderProblem(engine.ProblemBadRequest, w, err.Error())

			return
		}

		if format := f.engine.CN.NegotiateFormat(r); format != engine.FormatJSON {
			handleFormatNotSupported(w, format)

			return
		}

		f.engine.Serve(w, r,
			engine.ServeJSON(f.schemaCatalog),
			engine.ServeContentType(engine.MediaTypeJSONSchema), // JSON format, but specific mediatype.
		)
	}
}

// buildSchemasCatalog builds the aggregated schema catalog once, during start-up. Collections are
// kept in the exact order in which they are configured. A failure for one collection doesn't abort
// the others: the failing collection is included with status "failed" and a public error category.
//
// The catalog is built once and served from memory, so repeated requests on the same configuration
// baseline return an identical (stable) response.
func buildSchemasCatalog(e *engine.Engine, failures map[string]schemaFailure) schemasCatalog {
	collections := e.Config.OgcAPI.Features.Collections
	entries := make([]collectionSchemaEntry, 0, len(collections))

	for _, collection := range collections {
		entry := collectionSchemaEntry{
			CollectionID: collection.ID,
			Href:         collectionSchemaHref(e, collection.ID),
			Status:       schemaStatusAvailable,
			// explicit null instead of nil, so a failed entry always carries a "schema" member
			Schema: json.RawMessage("null"),
		}

		if failure, ok := failures[collection.ID]; ok {
			entry.Status = schemaStatusFailed
			entry.ErrorCategory = failure.Category
			log.Printf("schema generation for collection %s failed, including it as '%s' in "+
				"schema catalog: %v", collection.ID, failure.Category, failure.Err)
		} else {
			entry.Schema = renderCollectionSchema(e, collection.ID)
			if len(entry.Schema) == 0 {
				entry.Status = schemaStatusFailed
				entry.ErrorCategory = schemaErrorCategoryInternal
				entry.Schema = json.RawMessage("null")
			}
		}

		entries = append(entries, entry)
	}

	return schemasCatalog{
		Schema:      jsonSchemaDraft202012,
		ID:          e.Config.BaseURL.String() + schemasCatalogPath,
		Type:        "object",
		Version:     e.Config.Version,
		Collections: entries,
	}
}

// renderCollectionSchema returns the compacted JSON of the already pre-rendered single collection
// schema. Reusing the rendered output guarantees the aggregated catalog uses exactly the same JSON
// Schema and field type rules as the per-collection /collections/{collectionId}/schema endpoint.
func renderCollectionSchema(e *engine.Engine, collectionID string) json.RawMessage {
	// the catalog response is language-independent in structure, use the first configured language
	// which is always present and deterministic for a given configuration baseline
	key := engine.NewTemplateKey(schemaJSON,
		engine.WithInstanceName(collectionID),
		engine.WithInclude(fieldsIncludeJSON),
		engine.WithMediaTypeOverwrite(engine.MediaTypeJSONSchema),
		engine.WithLanguage(e.Config.AvailableLanguages[0].Tag),
	)

	rendered, err := e.GetRenderedTemplate(key)
	if err != nil {
		log.Printf("pre-rendered schema for collection %s not found: %v", collectionID, err)

		return nil
	}

	compacted := &bytes.Buffer{}
	if err := json.Compact(compacted, rendered); err != nil {
		log.Printf("failed to compact pre-rendered schema for collection %s: %v", collectionID, err)

		return nil
	}

	return json.RawMessage(compacted.Bytes())
}

func collectionSchemaHref(e *engine.Engine, collectionID string) string {
	return fmt.Sprintf("%s%s/%s%s", e.Config.BaseURL.String(), g.CollectionsPath, collectionID, schemasPath)
}
