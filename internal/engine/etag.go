package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

// negotiatedVaryHeaders are the request headers taken into account by GoKoala's
// content negotiation, hence advertised in the Vary response header (RFC 9110):
//   - Accept selects the output format, in addition to the 'f' query parameter
//   - Accept-Language selects the language, in addition to the 'lang' query parameter
//   - Cookie can select the language through the language cookie
//
// Note: 'Vary: Accept-Encoding' is added as needed by the compression middleware.
var negotiatedVaryHeaders = []string{HeaderAccept, HeaderAcceptLanguage, HeaderCookie}

// etagPartSeparator delimits the parts fed into the ETag hash so distinct
// combinations of media type, variant and representation bytes can never
// collapse into a single validator.
const etagPartSeparator = 0x1f

// generateETag computes a strong, stable ETag validator for the given representation.
//
// The validator is derived from (1) the negotiated media type, (2) the selected
// response variant and (3) the exact representation bytes. As a result identical
// requests always yield the same validator, while language, format or URL variants
// always get a distinct validator even if their representation bytes happen to match.
func generateETag(mediaType string, variantKey string, representation []byte) string {
	hash := sha256.New()
	hash.Write([]byte(mediaType))
	hash.Write([]byte{etagPartSeparator})
	hash.Write([]byte(variantKey))
	hash.Write([]byte{etagPartSeparator})
	hash.Write(representation)

	return `"` + hex.EncodeToString(hash.Sum(nil)) + `"`
}

// serveConditional writes the given representation with a stable ETag validator and
// honors conditional GET/HEAD requests through If-None-Match.
//
// An ETag already present on the response, for example one supplied by an upstream
// server through the reverse proxy, is never overwritten. When If-None-Match matches
// the current validator a 304 Not Modified is written without a response body,
// otherwise the full representation is served with a 200 OK.
func serveConditional(w http.ResponseWriter, r *http.Request, mediaType string, representation []byte, variantKey string) {
	headers := w.Header()

	etag := headers.Get(HeaderETag)
	if etag == "" {
		etag = generateETag(mediaType, variantKey, representation)
		headers.Set(HeaderETag, etag)
	}
	addVaryHeaders(w)

	if isGetOrHead(r) && matchesIfNoneMatch(r.Header.Get(HeaderIfNoneMatch), etag) {
		w.WriteHeader(http.StatusNotModified)

		return
	}

	writeResponse(w, mediaType, representation)
}

// variantKey identifies the selected response variant. Together with the negotiated
// media type and the exact representation bytes it determines the ETag, ensuring
// language, format and URL variants never share a validator.
func (s *serve) variantKey(r *http.Request) string {
	if s.templateKey != nil {
		key := *s.templateKey

		return fmt.Sprintf(
			"template:{directory:%s,name:%s,format:%s,mediaTypeOverwrite:%s,language:%s,instanceName:%s,include:%s}",
			key.Directory, key.Name, key.Format, key.MediaTypeOverwrite, key.Language.String(),
			key.InstanceName, key.Include)
	}

	return fmt.Sprintf("url:{path:%s,rawQuery:%s}", r.URL.Path, r.URL.RawQuery)
}

func isGetOrHead(r *http.Request) bool {
	return r.Method == http.MethodGet || r.Method == http.MethodHead
}

// matchesIfNoneMatch evaluates an If-None-Match header against the current ETag.
// Entity tags are compared with the weak comparison function (RFC 9110 8.8.3.2),
// a comma-separated list matches when any of its members matches, and the wildcard
// '*' matches every current representation.
func matchesIfNoneMatch(ifNoneMatchHeader string, currentETag string) bool {
	if strings.TrimSpace(ifNoneMatchHeader) == "" {
		return false
	}

	currentOpaqueTag := opaqueEntityTag(currentETag)
	for _, entityTag := range splitEntityTags(ifNoneMatchHeader) {
		if entityTag == "*" || opaqueEntityTag(entityTag) == currentOpaqueTag {
			return true
		}
	}

	return false
}

// opaqueEntityTag returns the quoted opaque part of an entity tag,
// stripping an optional weak validator prefix (W/).
func opaqueEntityTag(entityTag string) string {
	return strings.TrimPrefix(strings.TrimSpace(entityTag), "W/")
}

// splitEntityTags splits the comma-separated entity tags of an If-None-Match
// header. Quoted-string syntax is respected because commas may legally occur
// inside an entity tag's opaque value.
func splitEntityTags(header string) []string {
	entityTags := make([]string, 0)

	var current strings.Builder

	insideQuotes := false

	for i := range len(header) {
		character := header[i]

		switch {
		case character == '"':
			insideQuotes = !insideQuotes
			current.WriteByte(character)
		case character == ',' && !insideQuotes:
			if entityTag := strings.TrimSpace(current.String()); entityTag != "" {
				entityTags = append(entityTags, entityTag)
			}

			current.Reset()
		default:
			current.WriteByte(character)
		}
	}

	if entityTag := strings.TrimSpace(current.String()); entityTag != "" {
		entityTags = append(entityTags, entityTag)
	}

	return entityTags
}

// addVaryHeaders advertises the request headers used during content negotiation
// in Vary, without duplicating values already present.
func addVaryHeaders(w http.ResponseWriter) {
	existing := w.Header().Values(HeaderVary)
	for _, header := range negotiatedVaryHeaders {
		if !varyContains(existing, header) {
			w.Header().Add(HeaderVary, header)
		}
	}
}

func varyContains(existing []string, header string) bool {
	for _, value := range existing {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), header) {
				return true
			}
		}
	}

	return false
}
