# Local-only test database. Real deployments use managed/self-hosted PostgreSQL;
# this image exists because the development machine cannot pull postgres:15.
FROM dsh-harness-server:0.16.0
RUN apt-get update -qq \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq postgresql postgresql-contrib >/dev/null \
 && service postgresql start \
 && su postgres -c "psql -v ON_ERROR_STOP=1 -c \"CREATE ROLE sched LOGIN SUPERUSER PASSWORD 'sched';\"" \
 && su postgres -c "createdb -O sched sched" \
 && service postgresql stop \
 && echo 'host all all 0.0.0.0/0 scram-sha-256' >> /etc/postgresql/17/main/pg_hba.conf \
 && echo 'host all all ::/0 scram-sha-256' >> /etc/postgresql/17/main/pg_hba.conf \
 && rm -rf /var/lib/apt/lists/*
USER postgres
ENTRYPOINT []
EXPOSE 5432
CMD ["/usr/lib/postgresql/17/bin/postgres", "-D", "/var/lib/postgresql/17/main", "-c", "config_file=/etc/postgresql/17/main/postgresql.conf", "-c", "hba_file=/etc/postgresql/17/main/pg_hba.conf", "-c", "listen_addresses=0.0.0.0", "-c", "port=5432"]
