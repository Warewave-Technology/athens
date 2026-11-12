# syntax=docker/dockerfile:1.7
FROM node:22-alpine AS web
WORKDIR /src/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.26-alpine AS go
WORKDIR /src
ARG VERSION=dev COMMIT=none DATE=unknown
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
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

FROM gcr.io/distroless/static-debian12:nonroot AS mockidp
COPY --from=tools /out/mockidp /mockidp
USER 65532:65532
ENTRYPOINT ["/mockidp"]

FROM gcr.io/distroless/static-debian12:nonroot AS e2e
COPY --from=tools /out/e2e /e2e
USER 65532:65532
ENTRYPOINT ["/e2e", "-test.v", "-test.timeout=15m"]

FROM gcr.io/distroless/static-debian12:nonroot AS athens
COPY --from=go /out/athens /athens
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/athens"]
