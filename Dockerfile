#syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM tonistiigi/xx:1.6.1 AS xx

FROM --platform=$BUILDPLATFORM golang:1.24.4-alpine AS builder
WORKDIR /app

COPY --from=xx / /

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
# Set Golang build envs based on Docker platform string
ARG TARGETPLATFORM
RUN --mount=type=cache,target=/root/.cache \
  CGO_ENABLED=0 xx-go build -ldflags='-w -s' -tags lambda.norpc -trimpath -o aviary-integration-ses .


FROM alpine:3.21 AS rie
WORKDIR /app
ARG TARGETPLATFORM
ARG RIE_VERSION=v1.23
RUN <<EOT
  set -eux

  case "$TARGETPLATFORM" in
    'linux/amd64') export SUFFIX=x86_64 ;;
    'linux/arm64') export SUFFIX=arm64 ;;
    *) echo "Unsupported target: $TARGETPLATFORM" && exit 1 ;;
  esac

  wget \
    -O aws-lambda-rie \
    "https://github.com/aws/aws-lambda-runtime-interface-emulator/releases/download/$RIE_VERSION/aws-lambda-rie-$SUFFIX"
  chmod +x aws-lambda-rie
EOT

FROM gcr.io/distroless/static:nonroot AS base
WORKDIR /
COPY --from=builder /app/aviary-integration-ses .
ENTRYPOINT ["./aviary-integration-ses"]

FROM base AS local
COPY --from=rie /app/aws-lambda-rie .
ENTRYPOINT ["./aws-lambda-rie"]
CMD ["./aviary-integration-ses"]

FROM base
