# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.20 as builder

WORKDIR /src/
COPY go.* /src/
# Cache mod downloads
RUN go mod download -x

COPY cmd /src/cmd
COPY pkg /src/pkg

ARG GOOS=linux
ARG VERSION=v0.0.0-0.unknown

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-X github.com/blampe/longhorn-scheduler/pkg/consts.Version=${VERSION} -extldflags=-static" -o . -v ./cmd/...

FROM gcr.io/distroless/static:latest
COPY --from=builder /src/longhorn-scheduler-extender /longhorn-scheduler-extender
COPY --from=builder /src/longhorn-scheduler-admission /longhorn-scheduler-admission
USER nonroot
ENTRYPOINT ["/longhorn-scheduler-extender"]
