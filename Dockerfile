# Build stage
FROM golang:1.27-alpine AS build

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /src

# Copy dependency manifests first to cache downloaded modules.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/torrvia .

# Runtime stage: include only the binary, certificates, and time zone data.
FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /out/torrvia /torrvia

ENV TORRVIA_CONFIG=/config/config.toml \
    TZ=Asia/Shanghai

VOLUME ["/data", "/config"]
EXPOSE 8080

ENTRYPOINT ["/torrvia"]
