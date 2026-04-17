FROM golang:1.25-alpine AS build
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o api-gateway .

FROM scratch AS runtime

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /app/api-gateway /api-gateway
COPY --from=build /app/app.yaml /app.yaml
COPY --from=build /app/docs /docs

EXPOSE 9000

ENTRYPOINT ["/api-gateway"]
