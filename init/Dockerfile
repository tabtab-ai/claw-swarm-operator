FROM alpine:3.23

# Install dependencies as root
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk add --no-cache curl \
    && curl https://github.com/jqlang/jq/releases/download/jq-1.8.1/jq-linux-amd64 -L -o /usr/local/bin/jq \
    && chmod +x /usr/local/bin/jq

COPY ./scripts/auto_approve.sh /usr/local/bin

USER 1000:1000

