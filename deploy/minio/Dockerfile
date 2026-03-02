FROM golang:1.27.3-alpine3.24 AS build

RUN apk add --no-cache ca-certificates git

ARG MINIO_REF=RELEASE.2025-10-15T17-29-55Z
ARG MC_REF=RELEASE.2025-08-13T08-35-41Z

ENV CGO_ENABLED=0 GOTOOLCHAIN=local

RUN git clone --depth 1 --branch "${MINIO_REF}" https://github.com/minio/minio.git /src/minio
WORKDIR /src/minio
RUN go build -mod=readonly -trimpath -o /out/minio .

RUN git clone --depth 1 --branch "${MC_REF}" https://github.com/minio/mc.git /src/mc
WORKDIR /src/mc
RUN go build -mod=readonly -trimpath -o /out/mc .

FROM alpine:3.24.1

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 1000 minio \
    && adduser -D -u 1000 -G minio minio \
    && mkdir -p /data /licenses \
    && chown minio:minio /data

COPY --from=build /out/minio /usr/local/bin/minio
COPY --from=build /out/mc /usr/local/bin/mc
COPY --from=build /src/minio/LICENSE /licenses/minio-LICENSE
COPY --from=build /src/mc/LICENSE /licenses/mc-LICENSE

USER minio
WORKDIR /home/minio
EXPOSE 9000 9001
ENTRYPOINT ["minio"]
CMD ["server", "/data", "--console-address", ":9001"]
