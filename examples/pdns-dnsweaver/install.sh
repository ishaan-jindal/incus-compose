#!/usr/bin/env bash
set -euo pipefail

# Based on: https://computingforgeeks.com/running-powerdns-and-powerdns-admin-in-docker-containers/

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"

source "${SCRIPT_DIR}/.env"

if [ "${DB_PASSWORD}" == "superSecret" ] || [ "${DB_ROOT_PASSWORD}" == "superRootSecret" ] || [ "${API_KEY}" == "superAdminKeySecret" ]; then
  echo "Please adjust your .env first"
  exit 1
fi

mkdir -p "${SCRIPT_DIR}/work"

if [ ! -d "${SCRIPT_DIR}/work/dnsweaver" ]; then
  echo "Cloning dnsweaver to ${SCRIPT_DIR}/work/dnsweaver"
  pushd "${SCRIPT_DIR}/work" >/dev/null
  git clone https://github.com/maxfield-allison/dnsweaver.git
  popd >/dev/null
fi

if [ ! -f "${SCRIPT_DIR}/client.crt" ]; then
  echo "Creating a incus client certificate"
  INCUS_CONF="${SCRIPT_DIR}/work/incus" incus remote generate-certificate
  mv work/incus/client.crt "${SCRIPT_DIR}"
  mv work/incus/client.key "${SCRIPT_DIR}"
  rm -rf work/incus
  incus config trust add-certificate --name="dnsweaver" "${SCRIPT_DIR}/client.crt"
fi

echo "Creating configs from templates"
sed "s/DB_NAME_HERE/${DB_NAME}/g;
     s/DB_USER_HERE/${DB_USER}/g;
     s/DB_PASS_HERE/${DB_PASSWORD}/g;
     s/API_KEY_HERE/${API_KEY}/g" "${SCRIPT_DIR}/pdns/pdns.conf.template" > "${SCRIPT_DIR}/pdns/pdns.conf"

sed "s/DNSWEAVER_ZONE/${DNSWEAVER_PDNS_ZONE}/g;
     s/PDNS_IPV4_ADDRESS/${PDNS_IPV4_ADDRESS}/g" "${SCRIPT_DIR}/dnscrypt-proxy/forwarding-rules.txt.template" > "${SCRIPT_DIR}/dnscrypt-proxy/forwarding-rules.txt"

echo "Creating powerdns to copy the schema from it"
incus-compose up --no-start --no-deps pdns

echo "Copying the schema from pdns-auth"
incus-compose incus file pull pdns-auth/usr/local/share/doc/pdns/schema.mysql.sql "${SCRIPT_DIR}/work/schema.mysql.sql"

echo "Starting mariadb"
incus-compose up --no-deps mariadb

echo "Importing the PDNS schema"
incus-compose exec -e MYSQL_PWD="${DB_ROOT_PASSWORD}" mariadb mariadb -uroot "${DB_NAME}" < "${SCRIPT_DIR}/work/schema.mysql.sql"

echo "Creating the pdns-admin database: ${ADMIN_DB_NAME}"
incus-compose exec -e MYSQL_PWD="${DB_ROOT_PASSWORD}" mariadb mariadb -uroot -e \
  "DROP DATABASE IF EXISTS ${ADMIN_DB_NAME};
  CREATE DATABASE ${ADMIN_DB_NAME} CHARACTER SET utf8mb4;
  GRANT ALL PRIVILEGES ON ${ADMIN_DB_NAME}.* TO '${DB_USER}'@'%';
  FLUSH PRIVILEGES;"

echo "Starting pdns"
incus-compose up --no-deps pdns

echo "Creating your zone: ${DNSWEAVER_PDNS_ZONE}"
incus-compose exec pdns pdnsutil create-zone "${DNSWEAVER_PDNS_ZONE}"

echo "Starting the project"
incus-compose up
