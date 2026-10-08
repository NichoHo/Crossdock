# Go service: API, background jobs, migrations, synthetic operators, generators.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/crossdock ./cmd/crossdock

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 crossdock
COPY --from=build /out/crossdock /usr/local/bin/crossdock
COPY migrations /migrations
ENV CROSSDOCK_MIGRATIONS_DIR=/migrations
USER crossdock
EXPOSE 8080
ENTRYPOINT ["crossdock"]
CMD ["serve"]
