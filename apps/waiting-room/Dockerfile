# syntax=docker/dockerfile:1
# Build: docker build --platform linux/amd64 --build-arg APP_VERSION=1.0.0 -t waiting-room apps/waiting-room
FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG APP_VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${APP_VERSION}" -o /out/waiting-room ./cmd/waiting-room

# distroless/static ships CA certificates (Compute and Secret Manager APIs are HTTPS) and runs as nonroot.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/waiting-room /waiting-room
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/waiting-room"]
