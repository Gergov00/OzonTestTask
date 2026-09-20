package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/coder/websocket"

	"testozon/graph"
	"testozon/internal/auth"
	"testozon/internal/config"
	"testozon/internal/graphqlapi"
	"testozon/internal/loader"
	"testozon/internal/repository"
	"testozon/internal/repository/memory"
	"testozon/internal/repository/postgres"
	"testozon/internal/service"
	"testozon/internal/subscription"
)

const (
	commentBuffer  = 16
	shutdownPeriod = 10 * time.Second
)

type Dependencies struct {
	Repository repository.Repository
	Service    *service.Service
	Broker     *subscription.Broker
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load configuration: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}

func run(ctx context.Context, cfg config.Config) error {
	deps, closeRepository, err := buildDependencies(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeRepository()

	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}
	log.Printf("listening on %s with %s storage", listener.Addr(), cfg.StorageDriver)
	return serve(ctx, listener, deps)
}

func serve(ctx context.Context, listener net.Listener, deps Dependencies) error {
	operationsCtx, cancelOperations := context.WithCancel(context.Background())
	connections := newWebsocketTracker(transport.CoderWebsocketImplementation{
		AcceptOptions: websocket.AcceptOptions{},
	})
	server := &http.Server{
		Handler:           newHandlerWithWebsockets(deps, connections),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return operationsCtx
		},
	}
	defer func() {
		connections.beginShutdown()
		cancelOperations()
		connections.closeAll()
	}()
	errorsCh := make(chan error, 1)
	go func() {
		errorsCh <- server.Serve(listener)
	}()

	select {
	case err := <-errorsCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
	}

	connections.beginShutdown()
	cancelOperations()
	connections.closeAll()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownPeriod)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		<-errorsCh
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}
	if err := <-errorsCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP during shutdown: %w", err)
	}
	return nil
}

func buildDependencies(ctx context.Context, cfg config.Config) (Dependencies, func(), error) {
	var (
		store      repository.Repository
		closeStore = func() {}
	)
	switch cfg.StorageDriver {
	case config.StorageMemory:
		store = memory.New()
	case config.StoragePostgres:
		postgresStore, err := postgres.New(ctx, cfg.DatabaseURL)
		if err != nil {
			return Dependencies{}, nil, fmt.Errorf("open postgres repository: %w", err)
		}
		store = postgresStore
		closeStore = postgresStore.Close
	default:
		return Dependencies{}, nil, fmt.Errorf("unsupported storage driver %q", cfg.StorageDriver)
	}

	broker := subscription.New(commentBuffer)
	return Dependencies{
		Repository: store,
		Service:    service.New(store, broker, time.Now),
		Broker:     broker,
	}, closeStore, nil
}

func newHandler(deps Dependencies) http.Handler {
	connections := newWebsocketTracker(transport.CoderWebsocketImplementation{
		AcceptOptions: websocket.AcceptOptions{},
	})
	return newHandlerWithWebsockets(deps, connections)
}

func newHandlerWithWebsockets(deps Dependencies, connections *websocketTracker) http.Handler {
	executable := graph.NewExecutableSchema(graph.Config{
		Resolvers:  &graph.Resolver{Service: deps.Service, Broker: deps.Broker},
		Complexity: newComplexity(),
	})
	graphQL := handler.New(executable)
	graphQL.AddTransport(transport.Websocket{
		Implementation:        connections,
		InitTimeout:           10 * time.Second,
		KeepAlivePingInterval: 10 * time.Second,
	})
	graphQL.AddTransport(transport.Options{})
	graphQL.AddTransport(transport.GET{})
	graphQL.AddTransport(transport.POST{})
	graphQL.SetErrorPresenter(graphqlapi.ErrorPresenter)
	graphQL.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		return next(loader.WithNewCommentLoader(ctx, deps.Repository))
	})
	graphQL.Use(extension.Introspection{})
	graphQL.Use(extension.FixedComplexityLimit(200))

	mux := http.NewServeMux()
	mux.Handle("/query", connections.trackHandler(auth.Middleware(graphQL)))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/", playground.Handler("GraphQL playground", "/query"))
	return mux
}

func newComplexity() graph.ComplexityRoot {
	var complexity graph.ComplexityRoot
	complexity.Query.Posts = pageComplexity
	complexity.Post.Comments = pageComplexity
	complexity.Comment.Replies = pageComplexity
	return complexity
}

func pageComplexity(childComplexity, first int, _ *string) int {
	if first < 1 {
		first = 1
	} else if first > 100 {
		first = 100
	}
	maxInt := int(^uint(0) >> 1)
	if childComplexity < 0 || childComplexity > (maxInt-1)/first {
		return maxInt
	}
	return 1 + first*childComplexity
}

type websocketTracker struct {
	implementation transport.WebsocketImplementation

	mu          sync.Mutex
	connections map[*trackedWebsocket]struct{}
	closing     bool
	handlers    sync.WaitGroup
}

func newWebsocketTracker(implementation transport.WebsocketImplementation) *websocketTracker {
	return &websocketTracker{
		implementation: implementation,
		connections:    make(map[*trackedWebsocket]struct{}),
	}
}

func (t *websocketTracker) Accept(w http.ResponseWriter, r *http.Request, options transport.WebsocketAcceptOptions) (transport.WebsocketConn, error) {
	t.mu.Lock()
	if t.closing {
		t.mu.Unlock()
		return nil, errors.New("websocket server is shutting down")
	}
	t.mu.Unlock()

	connection, err := t.implementation.Accept(w, r, options)
	if err != nil {
		return nil, err
	}
	tracked := &trackedWebsocket{WebsocketConn: connection, tracker: t}

	t.mu.Lock()
	if t.closing {
		t.mu.Unlock()
		_ = connection.Close()
		return nil, errors.New("websocket server is shutting down")
	}
	t.connections[tracked] = struct{}{}
	t.mu.Unlock()
	return tracked, nil
}

func (t *websocketTracker) beginShutdown() {
	t.mu.Lock()
	t.closing = true
	t.mu.Unlock()
}

func (t *websocketTracker) closeAll() {
	t.mu.Lock()
	connections := make([]*trackedWebsocket, 0, len(t.connections))
	for connection := range t.connections {
		connections = append(connections, connection)
	}
	t.mu.Unlock()
	for _, connection := range connections {
		_ = connection.forceClose()
	}
	t.handlers.Wait()
}

func (t *websocketTracker) isClosing() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closing
}

func (t *websocketTracker) remove(connection *trackedWebsocket) {
	t.mu.Lock()
	if _, ok := t.connections[connection]; ok {
		delete(t.connections, connection)
	}
	t.mu.Unlock()
}

func (t *websocketTracker) trackHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "" {
			next.ServeHTTP(w, r)
			return
		}

		t.mu.Lock()
		if t.closing {
			t.mu.Unlock()
			http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
			return
		}
		t.handlers.Add(1)
		t.mu.Unlock()
		defer t.handlers.Done()
		next.ServeHTTP(w, r)
	})
}

type trackedWebsocket struct {
	transport.WebsocketConn
	tracker *websocketTracker
	once    sync.Once
}

func (c *trackedWebsocket) Close() error {
	err := c.WebsocketConn.Close()
	c.once.Do(func() { c.tracker.remove(c) })
	return err
}

func (c *trackedWebsocket) forceClose() error {
	return c.WebsocketConn.Close()
}

func (c *trackedWebsocket) WriteClose(closeCode int, message string) error {
	if c.tracker.isClosing() {
		return nil
	}
	return c.WebsocketConn.WriteClose(closeCode, message)
}

func (c *trackedWebsocket) NextReader() (int, io.Reader, error) {
	messageType, reader, err := c.WebsocketConn.NextReader()
	if err != nil {
		c.once.Do(func() { c.tracker.remove(c) })
	}
	return messageType, reader, err
}

func (c *trackedWebsocket) SetReadLimit(limit int64) {
	if limiter, ok := c.WebsocketConn.(transport.WebsocketReadLimiter); ok {
		limiter.SetReadLimit(limit)
	}
}

func (c *trackedWebsocket) SetReadDeadline(deadline time.Time) error {
	if deadliner, ok := c.WebsocketConn.(transport.WebsocketReadDeadliner); ok {
		return deadliner.SetReadDeadline(deadline)
	}
	return nil
}
