# syntax=docker/dockerfile:1@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89

FROM docker.io/library/golang:1.26.10-bookworm@sha256:5b1f78db1f54cc8cb46e652c51d3f854441c4310a449161b6fe9fb553edab505 AS gosu-build
ADD --keep-git-dir=true https://github.com/tianon/gosu.git#6456aaa0f3c854d199d0f037f068eb97515b7513 /src/gosu
WORKDIR /src/gosu
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOTOOLCHAIN=local GOMAXPROCS=2 \
  go build -p=1 -v -trimpath -ldflags="-d -w" -buildvcs=true -mod=readonly -o /out/gosu . \
  && go version -m /out/gosu \
  && /out/gosu --version

FROM docker.io/library/postgres:17.11-alpine3.24@sha256:e4cb93687b062cd25a0e8f672d85a7708235009d70d31f8a7698d9370092a9c4 AS pgvector-build
RUN apk add --no-cache gcc=15.2.0-r5 musl-dev=1.2.6-r3 make=4.4.1-r4 clang21=21.1.8-r3 llvm21-dev=21.1.8-r1
ADD --checksum=sha256:4c33cf053329784ba6d992d05c9588b93789e907a7511f20ff5a5a5b8a0703c1 https://codeload.github.com/pgvector/pgvector/tar.gz/778dacf20c07caf904557a88705142631818d8cb /tmp/pgvector.tar.gz
RUN mkdir /src \
  && tar -xzf /tmp/pgvector.tar.gz --strip-components=1 -C /src \
  && cd /src \
  && make -j2 OPTFLAGS="" PG_CONFIG=/usr/local/bin/pg_config \
  && make install OPTFLAGS="" PG_CONFIG=/usr/local/bin/pg_config DESTDIR=/out \
  && install -Dm0644 LICENSE /out/usr/share/doc/pgvector/LICENSE \
  && install -Dm0644 README.md /out/usr/share/doc/pgvector/README.md

FROM docker.io/library/postgres:17.11-alpine3.24@sha256:e4cb93687b062cd25a0e8f672d85a7708235009d70d31f8a7698d9370092a9c4
COPY --from=gosu-build /out/gosu /usr/local/bin/gosu
COPY --from=gosu-build /src/gosu/LICENSE /usr/share/doc/gosu/LICENSE
COPY --from=pgvector-build /out/usr/local/lib/postgresql/ /usr/local/lib/postgresql/
COPY --from=pgvector-build /out/usr/local/share/postgresql/extension/ /usr/local/share/postgresql/extension/
COPY --from=pgvector-build /out/usr/share/doc/pgvector/ /usr/share/doc/pgvector/
RUN install -d -o postgres -g postgres -m 0700 /var/lib/postgresql/data \
  && install -d -o postgres -g postgres -m 0775 /var/run/postgresql

ENV LANG=C.UTF-8
ENV LC_ALL=C.UTF-8
ENV PGDATA=/var/lib/postgresql/data

EXPOSE 5432

HEALTHCHECK --interval=10s --timeout=5s --start-period=30s --retries=5 \
  CMD ["pg_isready", "-U", "engram", "-d", "engram"]

USER 70:70
