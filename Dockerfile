# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26@sha256:6e59e17a20f13c102e49d82dffab66d41cffe6c266b225d82794328aea859ab5 AS build
ARG TARGETOS TARGETARCH VERSION=dev REVISION=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/prometheus/common/version.Version=${VERSION#v} -X github.com/prometheus/common/version.Revision=${REVISION} -X github.com/prometheus/common/version.Branch=main -X github.com/prometheus/common/version.BuildUser=docker" \
    -o /out/prometheus-noderings-sd ./cmd/prometheus-noderings-sd

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/prometheus-noderings-sd /bin/prometheus-noderings-sd
USER 65532:65532
EXPOSE 10064
ENTRYPOINT ["/bin/prometheus-noderings-sd"]
