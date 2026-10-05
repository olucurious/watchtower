# syntax=docker/dockerfile:1
# The build stages run on the build machine's platform and cross-compile,
# so multi-architecture images build without emulation.
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/ui/dist ./internal/ui/dist
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/watchtower ./cmd/watchtower

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/watchtower /usr/local/bin/watchtower
EXPOSE 8080
ENV WATCHTOWER_LISTEN=:8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=20s --retries=3 CMD ["watchtower", "healthcheck"]
ENTRYPOINT ["watchtower"]
CMD ["serve"]
