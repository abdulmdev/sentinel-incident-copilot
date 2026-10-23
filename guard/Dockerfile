# Sentinel Guard: one static binary on a distroless base (no shell, non-root).
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/sentinel-guard ./cmd/sentinel-guard

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="sentinel-guard" \
      org.opencontainers.image.description="Envelope protection for AI agents: no agent changes production unless policy or a human approved that exact change, once." \
      org.opencontainers.image.source="https://github.com/abdulmdev/sentinel-incident-copilot" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/sentinel-guard /sentinel-guard
USER 65532:65532
EXPOSE 8080 8443 9090
ENTRYPOINT ["/sentinel-guard"]
