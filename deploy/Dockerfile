FROM golang:1.25-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/sm-controller ./cmd/sm-controller/
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/sm-agent ./cmd/sm-agent/
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/sm-api ./cmd/sm-api/
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/stratonmesh ./cmd/stratonmesh/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates curl docker-cli docker-cli-compose git
COPY --from=builder /bin/sm-controller /bin/sm-controller
COPY --from=builder /bin/sm-agent /bin/sm-agent
COPY --from=builder /bin/sm-api /bin/sm-api
COPY --from=builder /bin/stratonmesh /bin/stratonmesh
CMD ["/bin/sm-controller"]
