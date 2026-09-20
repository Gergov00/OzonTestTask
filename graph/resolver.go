package graph

//go:generate go tool gqlgen generate

import (
	"testozon/internal/service"
	"testozon/internal/subscription"
)

type Resolver struct {
	Service *service.Service
	Broker  *subscription.Broker
}
