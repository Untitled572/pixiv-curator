ARG GO_VERSION=1.26.0
ARG BUILDPLATFORM=linux/amd64
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY . .
RUN go mod download
RUN mkdir -p /out/data && chown 65532:65532 /out/data
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -mod=readonly -buildvcs=false -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" -o /out/pixiv-curator ./cmd/api

FROM scratch
ARG VERSION=dev
LABEL org.opencontainers.image.title="Pixiv Curator" \
      org.opencontainers.image.source="https://github.com/Untitled572/pixiv-curator" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/pixiv-curator /pixiv-curator
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV PIXIV_CURATOR_ADDR=0.0.0.0:5722
ENV PIXIV_CURATOR_DATABASE_PATH=/data/curator.db
VOLUME ["/data"]
EXPOSE 5722
ENTRYPOINT ["/pixiv-curator"]
