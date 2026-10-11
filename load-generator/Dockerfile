# syntax=docker/dockerfile:1
# Build: docker build --platform linux/amd64 -t loadgen load-generator
FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=1.0.0
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/loadgen ./cmd/loadgen

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/loadgen /loadgen
USER nonroot:nonroot
ENTRYPOINT ["/loadgen"]
CMD ["agent"]
