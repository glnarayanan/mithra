# syntax=docker/dockerfile:1
# check=skip=SecretsUsedInArgOrEnv
#
# Mithra container image: one binary, SQLCipher-encrypted SQLite, no shell
# tooling beyond what Debian slim ships. The same image runs the app and the
# sandboxed PDF parser; docker-compose.yml decides which and how each is
# confined. See documentation/deploy-containers.md.

ARG GO_VERSION=1.27.3
ARG DEBIAN_RELEASE=trixie

# SQLCipher source, pinned to the v4.19.0 tag and checked against its commit
# so a moved tag cannot change what is compiled into the image.
FROM golang:${GO_VERSION}-${DEBIAN_RELEASE} AS sqlcipher-source
ARG SQLCIPHER_TAG=v4.19.0
ARG SQLCIPHER_COMMIT=c4b275a47932888216bade83aff2bbc73df0ff85
RUN git clone --quiet --depth 1 --branch "${SQLCIPHER_TAG}" https://github.com/sqlcipher/sqlcipher.git /sqlcipher \
 && test "$(git -C /sqlcipher rev-parse HEAD)" = "${SQLCIPHER_COMMIT}"

# Static SQLCipher amalgamation with the same feature set Mithra's plain build
# uses (FTS5 on, extension loading off), linked against the system OpenSSL.
FROM golang:${GO_VERSION}-${DEBIAN_RELEASE} AS sqlcipher
RUN apt-get update \
 && apt-get install -y --no-install-recommends tcl libssl-dev \
 && rm -rf /var/lib/apt/lists/*
COPY --from=sqlcipher-source /sqlcipher /sqlcipher
WORKDIR /sqlcipher
RUN ./configure --disable-shared --enable-static --with-tempstore=yes \
      CFLAGS="-O2 -DSQLITE_HAS_CODEC -DSQLITE_ENABLE_FTS5 -DSQLITE_OMIT_LOAD_EXTENSION -DSQLITE_EXTRA_INIT=sqlcipher_extra_init -DSQLITE_EXTRA_SHUTDOWN=sqlcipher_extra_shutdown -DSQLCIPHER_LOG_LEVEL_DEFAULT=SQLCIPHER_LOG_NONE" \
      LDFLAGS="-lcrypto" \
 && make -j"$(nproc)" libsqlite3.a sqlite3.h

FROM golang:${GO_VERSION}-${DEBIAN_RELEASE} AS build
RUN apt-get update \
 && apt-get install -y --no-install-recommends libssl-dev \
 && rm -rf /var/lib/apt/lists/*
COPY --from=sqlcipher /sqlcipher/libsqlite3.a /sqlcipher/sqlite3.h /sqlcipher/
ENV CGO_ENABLED=1 \
    GOFLAGS=-tags=sqlite_fts5,sqlite_omit_load_extension,libsqlite3 \
    CGO_CFLAGS=-I/sqlcipher \
    CGO_LDFLAGS="-L/sqlcipher -lsqlite3 -lcrypto"
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
ARG VERSION=dev
RUN go build -trimpath -ldflags "-s -w -X main.buildVersion=${VERSION}" -o /out/mithra ./cmd/mithra \
 && /out/mithra version

# `docker build --target test .` runs the suite against SQLCipher, including
# the encryption tests the plain build skips.
FROM build AS test
RUN go vet ./... && go test -count=1 ./...

FROM debian:${DEBIAN_RELEASE}-slim AS runtime
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata libssl3t64 \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --system --gid 10001 mithra \
 && useradd --system --uid 10001 --gid mithra --home-dir /data --no-create-home --shell /usr/sbin/nologin mithra \
 && useradd --system --uid 10002 --gid mithra --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin mithra-pdf \
 && install -d -o mithra -g mithra -m 0700 /data /backups \
 && install -d -o mithra-pdf -g mithra -m 0750 /run/mithra-pdf
COPY --from=build /out/mithra /usr/local/bin/mithra

ENV MITHRA_PROXY_MODE=container \
    MITHRA_ADDR=0.0.0.0:8080 \
    MITHRA_DB=/data/mithra/mithra.sqlite3 \
    MITHRA_SOURCE_DIR=/data/mithra/sources \
    MITHRA_LOCK_FILE=/data/.mithra.lock \
    MITHRA_BACKUP_DIR=/backups \
    MITHRA_PDF_PARSER_SOCKET=/run/mithra-pdf/pdf-parser.sock \
    MITHRA_CREDENTIALS_DIRECTORY=/run/secrets \
    MITHRA_MASTER_KEY_FILE=/run/secrets/master_key \
    MITHRA_PLUNK_KEY_FILE=/run/secrets/plunk_key

USER mithra:mithra
WORKDIR /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/mithra", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/mithra"]
CMD ["serve"]
