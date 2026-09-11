package transport

import (
	"io"
	"net/http"

	"github.com/huzaifa678/SAAS-services/errors"
	"github.com/huzaifa678/SAAS-services/service"
)

// NewHandler builds the terminal http.Handler for a proxied backend. It reads
// the incoming request, hands it to the ForwardService (whose proxy chain adds
// circuit breaking and the upstream call), and writes the upstream response
// back to the client.
//
// This single handler replaces the former go-kit trio of Make*Endpoint,
// Decode/Encode*Request and New{REST,GraphQL}HTTPHandler: the gateway forwards
// raw HTTP, so there is nothing to decode into a domain request. Cross-cutting
// concerns (auth, rate limiting, logging, tracing) are applied as standard
// net/http middleware around this handler.
//
// BackendForward godoc
// @Summary Gateway forward endpoint
// @Description Forwards the request to the backing service through the API Gateway
// @Accept json
// @Produce json
// @Param Authorization header string false "Bearer JWT token"
// @Param request body object false "Request payload"
// @Success 200 {object} endpoint.ForwardResponseSwagger
// @Failure 401 {object} endpoint.ForwardResponseSwagger
// @Failure 503 {object} endpoint.ForwardResponseSwagger
func NewHandler(svc service.ForwardService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			errors.EncodeError(r.Context(), err, w)
			return
		}

		respBody, status, err := svc.Forward(r.Context(), body, r.Header, r.URL.Path, r.Method)
		if err != nil {
			errors.EncodeError(r.Context(), err, w)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(respBody)
	})
}
