# syntax=docker/dockerfile:1@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89

# Project-owned PostgreSQL image: fixed base, fixed database and extension
# versions, and the upstream OCI initialization contract.
FROM cgr.dev/chainguard/wolfi-base@sha256:02dab76bd852a70556b5b2002195c8a5fdab77d323c433bf6642aab080489795

RUN apk --repositories-file /dev/null --repository https://packages.wolfi.dev/os --no-cache add \
  bash=5.3-r12 \
  gosu=1.19-r13 \
  postgresql-17=17.10-r1 \
  postgresql-17-client=17.10-r1 \
  postgresql-17-contrib=17.10-r1 \
  postgresql-17-oci-entrypoint=17.10-r1 \
  pgvector-17=0.8.1-r0 \
  && addgroup -S -g 70 postgres \
  && adduser -S -D -H -u 70 -G postgres postgres \
  && install -d -o postgres -g postgres -m 0700 /var/lib/postgresql/data \
  && install -d -o postgres -g postgres -m 0775 /var/run/postgresql

ENV LANG=C.UTF-8
ENV LC_ALL=C.UTF-8
ENV PGDATA=/var/lib/postgresql/data
ENV PATH=/usr/lib/postgresql17/bin:/usr/local/sbin:/usr/local/bin:/usr/bin:/usr/sbin:/sbin:/bin

EXPOSE 5432

HEALTHCHECK --interval=10s --timeout=5s --start-period=30s --retries=5 \
  CMD ["pg_isready", "-U", "engram", "-d", "engram"]

USER 70:70
ENTRYPOINT ["/usr/bin/docker-entrypoint.sh"]
CMD ["postgres"]
