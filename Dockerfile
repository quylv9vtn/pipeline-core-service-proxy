# Multi-stage build mirroring the KFP apiserver Dockerfile conventions.
FROM golang:1.24-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /bin/pipeline-core-service .

FROM debian:stable
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=builder /bin/pipeline-core-service /bin/pipeline-core-service
ENV REGIONS_CONFIG_PATH=/etc/core-gateway/regions.json \
    LOG_LEVEL=info \
    PORT=8080
RUN useradd --system --uid 1000 gateway
USER 1000
EXPOSE 8080
CMD ["/bin/sh", "-c", "/bin/pipeline-core-service"]
