# The image check pods run: bash, kubectl and jq, nothing else. The controller mounts the script
# and runs it as the command; kubectl authenticates with the pod's service account token.
FROM alpine:3.22

ARG TARGETARCH
ARG KUBECTL_VERSION=v1.35.3

RUN apk add --no-cache bash jq ca-certificates \
 && apk add --no-cache --virtual .fetch curl \
 && curl -fsSLo /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
 && echo "$(curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl.sha256")  /usr/local/bin/kubectl" | sha256sum -c - \
 && chmod 0755 /usr/local/bin/kubectl \
 && apk del .fetch \
 && rm -f /usr/bin/wget /usr/bin/nc /usr/bin/telnet /usr/bin/ftpget /usr/bin/ftpput /usr/bin/tftp

USER 65532:65532
WORKDIR /tmp

CMD ["bash"]
