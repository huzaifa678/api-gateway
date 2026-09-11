# Refactor: proxy service layer + dropping go-kit

_September 2026_

This note records why the gateway's service layer and middleware stack were
rewritten, what the old code looked like, and why go-kit was pulled out
entirely rather than kept around. Written down so the next person doesn't have
to reverse-engineer the intent from the diff.

## The gateway, in one line

It takes an HTTP request, checks a token, checks a rate limit, and forwards the
raw bytes to one of three backends (auth, subscription, billing). HTTP in, HTTP
out. That's the whole job, and it's the fact the rest of this doc keeps coming
back to.

## What the service layer used to be

`forward.service.go` had a `ForwardService` interface with one method,
implemented by a struct that held a single closure. That closure did
everything at once: build the outbound request, set headers, inject trace
context, wrap the call in a circuit breaker, run it, and swap in a fallback
body on failure.

The problem was the breaker. It was created *inside* the closure, so a fresh
`gobreaker.CircuitBreaker` was built on every request:

```go
s.forward = func(...) {
    wrapped := circuit.WrapWithBreaker(func(ctx) {...}, serviceName, cbCfg)
    res, err := wrapped(ctx)
    ...
}
```

A circuit breaker trips by counting consecutive failures. If you throw it away
and build a new one each call, the count is always zero and the circuit can
never open. So the breaker was, in practice, dead code — it added the failure
path (fallback on error) but never the thing it exists for (shedding load off a
sick backend).

### What it is now

Split into two types that both satisfy `ForwardService`, i.e. the proxy
pattern:

- `httpForwarder` — the real subject. Does the HTTP call and trace propagation.
  No resilience logic.
- `breakerProxy` — a protection proxy that holds the breaker as a field
  (built once, in the constructor) and wraps `httpForwarder`. On failure it
  returns the fallback.

`NewForwardService` composes them: `breakerProxy → httpForwarder → upstream`.
Same interface, same constructor signature, so nothing upstream had to change.
Because the breaker now lives for the life of the process, failure counts
accumulate and the circuit actually trips. There's a regression test for it
(`TestBreakerProxy_TripsAfterThreshold`) that asserts the backend stops being
called once the circuit is open.

The payoff beyond the bug fix: the breaker is now independently testable, and
if we ever want a caching proxy or a retry proxy it's another struct in the
chain, not another branch inside one closure.

## Adding a cache proxy

The "another struct in the chain" line above stopped being hypothetical: there's
now a `cacheProxy` too. It wraps the breaker proxy from the outside, so the chain
reads `cacheProxy → breakerProxy → httpForwarder`. A cache hit returns before the
breaker or the network is ever touched.

It's deliberately conservative, because a gateway that hands back the wrong
cached bytes is worse than one that doesn't cache at all:

- **GET only.** Auth and subscription talk GraphQL over POST, so they're never
  cached — mutations must not be. Billing's REST GETs are the real target.
- **2xx only.** Errors aren't cached.
- **Keyed per caller.** The key is the request path plus a hash of the
  `Authorization` token. Two users hitting `/api/billing/invoices` get separate
  cache entries, so nobody reads anyone else's data. The token is hashed, not
  stored in the key.
- **Off by default, TTL-bounded.** `cache.ttlSeconds` in `app.yaml`; `0` (or a
  nil Redis client) makes `NewCachingProxy` return the wrapped service untouched,
  so the proxy isn't even in the chain.

Redis errors on read are treated as a miss; errors on write are ignored. The
cache never fails a request.

Wiring stayed out of the constructor on purpose. `NewForwardService` didn't
change — `main.go` wraps its result with `NewCachingProxy`. That's the whole
point of the pattern: you add behaviour by composing from outside, not by
editing the thing you're wrapping.

## What the middleware stack used to be

Everything hung off go-kit's endpoint abstraction:

```go
type Endpoint func(ctx context.Context, request interface{}) (interface{}, error)
```

The flow per route was: an HTTP handler decoded the request into a
`ForwardRequest` struct, passed it through a stack of endpoint middleware
(logging, rate limit, JWT, tracing), reached a `Make*Endpoint` that called the
service, then an encoder wrote the `ForwardResponse` back out. Auth middleware
recovered the typed request with `request.(endpoint.ForwardRequest)`.

## Why go-kit was removed completely

go-kit's endpoint layer earns its keep when one piece of business logic has to
be served over several transports — HTTP today, gRPC or NATS tomorrow — with
`Decode`/`Encode` adapting each one. The `interface{}` request and response are
the price you pay for that transport independence.

We never used any of it. The gateway speaks HTTP on both sides and forwards raw
bytes; the "business object" *is* the HTTP request. So the
decode → endpoint → encode round trip was pure indirection, and it cost us
things:

- **`interface{}` everywhere and unchecked type assertions.** The JWT and
  Keycloak middleware did `request.(ForwardRequest)`. Wire a route up wrong and
  it panics at runtime instead of failing to compile.
- **Two middleware idioms in one service.** CORS was already a plain
  `func(http.Handler) http.Handler`. Everything else was go-kit
  `endpoint.Middleware`. New readers had to learn both.
- **A dependency and a layer for a feature we don't have.** Transport
  independence we weren't using.

The replacement is the standard library: `http.Handler` plus
`func(http.Handler) http.Handler` middleware. Auth, rate limiting, logging and
tracing are all wrappers now; the user ID travels in the request context via
`r.WithContext`. The decode/encode/endpoint trio collapsed into one
`transport.NewHandler(svc)` that reads the body, calls `Forward`, and writes the
response. `main.go` composes each route with a tiny `chain` helper, keeping the
same execution order the go-kit stack had (tracing → auth → rate limit →
logging → handler). go-kit is gone from `go.mod`.

Net effect: fewer moving parts, no runtime type assertions, one middleware
style, and a router any Go dev can read without knowing a framework.

## File layout change

- `endpoint/` — deleted the six go-kit files; only `swagger.endpoint.go`
  (a response DTO for the generated docs) remains.
- `middleware/` — new. `logging.go`, `ratelimit.go`, `tracing.go` as
  net/http middleware.
- `interceptor/` — `jwt` and `keycloak` rewritten as net/http middleware.
- `transport/` — deleted `graphql.transport.go` and `rest.transpost.go`;
  added `handler.go`. `cors.go` unchanged.
- `service/forward.service.go` — the proxy split described above.
- `service/cache.proxy.go` — new. The Redis GET cache proxy.

## Also fixed along the way

The `logging` package didn't compile against otel/log v0.22.0 —
`log.String` / `log.StringValue` had moved to the `attribute` package
(`Record` now takes `attribute.Value` / `attribute.KeyValue`). Swapped the
calls in `logging.bridge.go` and `logging.span.go`. This was a pre-existing
break, unrelated to the refactor, but it blocked `main` from building so it had
to go.

## Left alone on purpose

Two pre-existing quirks were kept so this stayed a refactor and not a
behavior change. Worth fixing later, separately:

- The rate limiter reads the user ID from the context with a bare string key
  (`ctx.Value("userId")`), while the JWT middleware stores it under a typed
  `contextKey`. Different keys, so the lookup never matches and every caller is
  rate-limited as `"anonymous"`. Production uses Keycloak anyway, which stores
  claims under a different key again.
- The CORS allowlist check (`allowed[origin] == struct{}{}`) is always true, so
  every origin gets the CORS headers. There's a test that documents this and
  will fail loudly the day someone fixes it.
