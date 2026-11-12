# syntax=docker/dockerfile:1.7
# Web and Go stages run on the build platform and cross-compile, so
# multi-arch images build without emulation.
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
# pnpm at the version pinned in packageManager; Node 25+ ships no corepack
RUN npm install -g --no-fund --no-audit "$(node -p 'require("./package.json").packageManager')" && \
    pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS go
WORKDIR /src
ARG VERSION=dev COMMIT=none DATE=unknown TARGETOS TARGETARCH
ENV CGO_ENABLED=0 GOFLAGS=-trimpath GOOS=$TARGETOS GOARCH=$TARGETARCH
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN go build -ldflags "-s -w \
      -X github.com/Warewave-Technology/athens/internal/version.Version=${VERSION} \
      -X github.com/Warewave-Technology/athens/internal/version.Commit=${COMMIT} \
      -X github.com/Warewave-Technology/athens/internal/version.BuildDate=${DATE}" \
      -o /out/athens ./cmd/athens

# Test helpers, built only with --target mockidp / --target e2e.
FROM go AS tools
RUN go build -ldflags "-s -w" -o /out/mockidp ./hack/mockidp && \
    go test -c -tags e2e -o /out/e2e ./test/e2e

FROM gcr.io/distroless/static-debian13:nonroot AS mockidp
COPY --from=tools /out/mockidp /mockidp
USER 65532:65532
ENTRYPOINT ["/mockidp"]

FROM gcr.io/distroless/static-debian13:nonroot AS e2e
COPY --from=tools /out/e2e /e2e
USER 65532:65532
ENTRYPOINT ["/e2e", "-test.v", "-test.timeout=15m"]

FROM gcr.io/distroless/static-debian13:nonroot AS athens
COPY LICENSE NOTICE /licenses/
COPY --from=go /out/athens /athens
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/athens"]
