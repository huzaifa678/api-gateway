package endpoint

import (
	"context"

	"github.com/go-kit/kit/endpoint"
	"github.com/huzaifa678/SAAS-services/service"
)

func MakeBillingEndpoint(s service.ForwardService) endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		req := request.(ForwardRequest)

		body, status, err := s.Forward(ctx, req.Body, req.Header, req.Path, req.Method)
		if err != nil {
			return ForwardResponse{
				Error:  err.Error(),
				Status: status,
			}, nil
		}

		return ForwardResponse{
			Body:   body,
			Status: status,
		}, nil
	}
}
