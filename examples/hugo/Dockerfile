ARG BASE_URL

FROM docker.io/alpine:latest AS builder

RUN echo "${BASE_URL}"

RUN apk add --no-cache hugo
WORKDIR /site
COPY site/ .
RUN hugo build -b "${BASE_URL}"

FROM docker.io/nginxinc/nginx-unprivileged:alpine
COPY --from=builder /site/public /usr/share/nginx/html
