# Build environment
# -----------------
FROM --platform=$BUILDPLATFORM golang:1.25.6-bookworm AS builder
LABEL stage=builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

COPY main.go main.go
COPY apis/ apis/
COPY internal/ internal/

# Build
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /bin/manager main.go

# Deployment environment
# ----------------------
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /bin/manager /bin/manager

EXPOSE 8080 8081

USER nonroot:nonroot

ENTRYPOINT ["/bin/manager"]
