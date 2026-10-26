package gw

import (
	"net/http"

	"yunshu.dev/core/internal/apierr"
)

// MatchEndpoint performs P03: path and method recognition. It has no I/O and
// no dependency on the body.
//
// Unknown paths and known paths with the wrong method both return 404
// unknown_endpoint. The error-code registry has no method_not_allowed code;
// using 405 would emit a status the client contract does not describe.
func MatchEndpoint(r *http.Request) (Endpoint, Protocol, *apierr.APIError) {
	switch r.URL.Path {
	case string(EndpointChat):
		if r.Method != http.MethodPost {
			return "", "", apierr.New(apierr.CodeUnknownEndpoint)
		}
		return EndpointChat, ProtocolOpenAI, nil
	case string(EndpointMessages):
		if r.Method != http.MethodPost {
			return "", "", apierr.New(apierr.CodeUnknownEndpoint)
		}
		return EndpointMessages, ProtocolAnthropic, nil
	case string(EndpointImages):
		if r.Method != http.MethodPost {
			return "", "", apierr.New(apierr.CodeUnknownEndpoint)
		}
		return EndpointImages, ProtocolOpenAI, nil
	case string(EndpointEmbeddings):
		if r.Method != http.MethodPost {
			return "", "", apierr.New(apierr.CodeUnknownEndpoint)
		}
		return EndpointEmbeddings, ProtocolOpenAI, nil
	case string(EndpointModels):
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			return "", "", apierr.New(apierr.CodeUnknownEndpoint)
		}
		return EndpointModels, ProtocolOpenAI, nil
	default:
		return "", "", apierr.New(apierr.CodeUnknownEndpoint)
	}
}

// RequiresBody reports whether the endpoint carries a JSON request body.
func RequiresBody(e Endpoint) bool { return e != EndpointModels }
