package engine

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

const (
	HeaderOrigin                      = "Origin"
	HeaderVary                        = "Vary"
	HeaderAccessControlRequestMethod  = "Access-Control-Request-Method"
	HeaderAccessControlRequestHeaders = "Access-Control-Request-Headers"
	HeaderAccessControlAllowOrigin    = "Access-Control-Allow-Origin"
	HeaderAccessControlAllowMethods   = "Access-Control-Allow-Methods"
	HeaderAccessControlAllowHeaders   = "Access-Control-Allow-Headers"
	HeaderAccessControlExposeHeaders  = "Access-Control-Expose-Headers"
	HeaderAccessControlMaxAge         = "Access-Control-Max-Age"

	// corsMaxAgeSeconds matches the max-age used by the wildcard CORS configuration.
	corsMaxAgeSeconds = 24 * 60 * 60
)

// corsAllowedPreflightMethods contains the methods permitted in CORS preflight requests,
// OPTIONS itself is the preflight method and doesn't need to be advertised.
var corsAllowedPreflightMethods = []string{http.MethodGet, http.MethodHead}

// validateCORSAllowedOrigins validates a list of exact, whitelisted CORS origins.
// Only http/https origins consisting of a scheme, host and optional port are accepted.
// Paths, query strings, fragments, wildcards and duplicate entries are rejected so that
// misconfiguration is reported during startup instead of silently weakening the whitelist.
func validateCORSAllowedOrigins(origins []string) ([]string, error) {
	seen := make(map[string]struct{}, len(origins))
	validated := make([]string, 0, len(origins))
	for _, origin := range origins {
		if err := validateCORSAllowedOrigin(origin); err != nil {
			return nil, err
		}
		if _, exists := seen[origin]; exists {
			return nil, fmt.Errorf("invalid CORS allowed origin %q: duplicate entry", origin)
		}
		seen[origin] = struct{}{}
		validated = append(validated, origin)
	}
	return validated, nil
}

func validateCORSAllowedOrigin(origin string) error {
	if strings.TrimSpace(origin) == "" {
		return fmt.Errorf("invalid CORS allowed origin %q: origin is empty", origin)
	}
	if strings.ContainsAny(origin, " \t\r\n") {
		return fmt.Errorf("invalid CORS allowed origin %q: whitespace is not allowed", origin)
	}
	if strings.Contains(origin, "*") {
		return fmt.Errorf("invalid CORS allowed origin %q: wildcards are not allowed, configure exact origins only", origin)
	}
	parsedOrigin, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("invalid CORS allowed origin %q: %w", origin, err)
	}
	return validateParsedOrigin(origin, parsedOrigin)
}

func validateParsedOrigin(origin string, parsedOrigin *url.URL) error {
	switch {
	case parsedOrigin.Scheme != "http" && parsedOrigin.Scheme != "https":
		return fmt.Errorf("invalid CORS allowed origin %q: only http and https schemes are allowed", origin)
	case parsedOrigin.Host == "":
		return fmt.Errorf("invalid CORS allowed origin %q: host is missing", origin)
	case parsedOrigin.Opaque != "":
		return fmt.Errorf("invalid CORS allowed origin %q: opaque URLs are not allowed", origin)
	case parsedOrigin.User != nil:
		return fmt.Errorf("invalid CORS allowed origin %q: userinfo is not allowed in an origin", origin)
	case parsedOrigin.Path != "" || parsedOrigin.RawPath != "":
		return fmt.Errorf("invalid CORS allowed origin %q: origins must not contain a path", origin)
	case parsedOrigin.RawQuery != "":
		return fmt.Errorf("invalid CORS allowed origin %q: origins must not contain a query string", origin)
	case parsedOrigin.Fragment != "" || parsedOrigin.RawFragment != "":
		return fmt.Errorf("invalid CORS allowed origin %q: origins must not contain a fragment", origin)
	case parsedOrigin.String() != origin:
		return fmt.Errorf("invalid CORS allowed origin %q: origin is not in canonical scheme://host[:port] form", origin)
	}
	return nil
}

// corsOriginWhitelistMiddleware returns CORS middleware restricted to a whitelist of exact origins.
//
// Requests without an Origin header are passed through unchanged so regular server-to-server
// calls keep their existing behavior. Requests from an allowed origin receive the exact origin
// in Access-Control-Allow-Origin together with a Vary: Origin header. Preflight (OPTIONS)
// requests from allowed origins are answered with 204 and only permit GET/HEAD with the
// existing CORS headers, while preflight requests from unknown origins or requesting
// unsupported methods/headers receive a 403 RFC 7807 problem response.
func corsOriginWhitelistMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allowedOriginSet := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowedOriginSet[origin] = struct{}{}
	}
	allowedHeaderSet := map[string]struct{}{
		HeaderOrigin: {},
		http.CanonicalHeaderKey(HeaderRequestedWith): {},
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get(HeaderOrigin)
			if origin == "" {
				// no Origin header: regular server-to-server request, keep existing behavior
				next.ServeHTTP(w, r)
				return
			}
			// always advertise that the response varies on Origin, allowed or not
			headers := w.Header()
			headers.Add(HeaderVary, HeaderOrigin)

			if _, allowed := allowedOriginSet[origin]; !allowed {
				if isCORSPreflight(r) {
					RenderProblem(ProblemForbidden, w,
						fmt.Sprintf("CORS preflight request from origin %q is not allowed", origin))
					return
				}
				// actual request from an unknown origin: no CORS allow headers, handle normally
				next.ServeHTTP(w, r)
				return
			}

			if isCORSPreflight(r) {
				// validate the preflight before emitting any allow headers, a rejected
				// preflight only carries the 403 problem response
				if err := validateCORSPreflight(r, allowedHeaderSet); err != nil {
					RenderProblem(ProblemForbidden, w, err.Error())
					return
				}
				headers.Set(HeaderAccessControlAllowOrigin, origin)
				headers.Set(HeaderAccessControlAllowMethods, strings.Join(corsAllowedPreflightMethods, ", "))
				if requestedHeaders := parseHeaderList(r.Header.Get(HeaderAccessControlRequestHeaders)); len(requestedHeaders) > 0 {
					headers.Set(HeaderAccessControlAllowHeaders, strings.Join(requestedHeaders, ", "))
				}
				headers.Set(HeaderAccessControlMaxAge, strconv.Itoa(corsMaxAgeSeconds))
				w.WriteHeader(http.StatusNoContent)
				return
			}

			// actual request from an allowed origin: echo the exact origin and expose CORS headers
			headers.Set(HeaderAccessControlAllowOrigin, origin)
			headers.Set(HeaderAccessControlExposeHeaders, strings.Join([]string{HeaderContentCrs, HeaderLink}, ", "))
			next.ServeHTTP(w, r)
		})
	}
}

func isCORSPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get(HeaderAccessControlRequestMethod) != ""
}

func validateCORSPreflight(r *http.Request, allowedHeaderSet map[string]struct{}) error {
	requestedMethod := strings.ToUpper(r.Header.Get(HeaderAccessControlRequestMethod))
	if !slices.Contains(corsAllowedPreflightMethods, requestedMethod) {
		return fmt.Errorf("CORS preflight request for method %q is not allowed, only GET and HEAD are supported", requestedMethod)
	}
	for _, requestedHeader := range parseHeaderList(r.Header.Get(HeaderAccessControlRequestHeaders)) {
		if _, allowed := allowedHeaderSet[http.CanonicalHeaderKey(requestedHeader)]; !allowed {
			return fmt.Errorf("CORS preflight request for header %q is not allowed", requestedHeader)
		}
	}
	return nil
}

// parseHeaderList splits a comma-separated header value into trimmed, non-empty entries.
func parseHeaderList(headerList string) []string {
	result := make([]string, 0)
	for _, header := range strings.Split(headerList, ",") {
		if header = strings.TrimSpace(header); header != "" {
			result = append(result, header)
		}
	}
	return result
}
