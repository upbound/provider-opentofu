FROM alpine:3.22.2
RUN apk --no-cache add ca-certificates bash git curl
ARG TARGETOS
ARG TARGETARCH

ENV OPENTOFU_VERSION=1.11.13
ENV TF_IN_AUTOMATION=1
ENV TF_PLUGIN_CACHE_DIR=/tofu/plugin-cache

ADD "bin/${TARGETOS}_${TARGETARCH}/provider" /usr/local/bin/crossplane-opentofu-provider
ADD .gitconfig .gitconfig

# The OpenTofu release signing key, pinned in-tree. Fetching it at build time
# would make the signature check circular, so it is committed next to this
# Dockerfile. Fingerprint: E3E6E43D84CB852EADB0051D0C0AF313E5FD9F80
ADD opentofu-release.asc /tmp/opentofu-release.asc

# Download OpenTofu, verify the GPG signature on its published checksums, then
# verify the archive against those checksums. Every step must succeed: an
# unverified, tampered or partial download has to fail the build, never ship.
#
# The final `tofu version` check guards against publishing a binary that does
# not match the pin above, which is how upbound/provider-opentofu#130 shipped
# 1.10.0 from a tree that said 1.10.8.
#
# /tofu is owned by UID 2000, and group-writable, because Crossplane overrides
# this image's USER: the DeploymentRuntimeConfig it generates runs the provider
# as 2000:2000. Group write additionally covers deployments that keep the
# default UID but set an fsGroup.
RUN set -eu \
  && apk --no-cache add --virtual .tofu-verify gnupg \
  && url="https://github.com/opentofu/opentofu/releases/download/v${OPENTOFU_VERSION}" \
  && archive="tofu_${OPENTOFU_VERSION}_${TARGETOS}_${TARGETARCH}.zip" \
  && sums="tofu_${OPENTOFU_VERSION}_SHA256SUMS" \
  && cd /tmp \
  && curl -fsSL "${url}/${archive}" -o "${archive}" \
  && curl -fsSL "${url}/${sums}" -o "${sums}" \
  && curl -fsSL "${url}/${sums}.gpgsig" -o "${sums}.gpgsig" \
  && export GNUPGHOME=/tmp/gnupg \
  && mkdir -p "${GNUPGHOME}" \
  && chmod 700 "${GNUPGHOME}" \
  && gpg --batch --quiet --import /tmp/opentofu-release.asc \
  && gpg --batch --status-fd 1 --verify "${sums}.gpgsig" "${sums}" \
     | grep -q '^\[GNUPG:\] VALIDSIG E3E6E43D84CB852EADB0051D0C0AF313E5FD9F80' \
  && awk -v f="${archive}" '$2 == f { print }' "${sums}" > expected.sha256 \
  && test -s expected.sha256 \
  && sha256sum -c expected.sha256 \
  && unzip -o -d /usr/local/bin "${archive}" tofu \
  && chmod +x /usr/local/bin/tofu \
  && tofu version | head -n1 | grep -qx "OpenTofu v${OPENTOFU_VERSION}" \
  && rm -rf "/tmp/${archive}" "/tmp/${sums}" "/tmp/${sums}.gpgsig" \
     /tmp/expected.sha256 /tmp/opentofu-release.asc "${GNUPGHOME}" \
  && apk del .tofu-verify \
  && mkdir -p ${TF_PLUGIN_CACHE_DIR} \
  && chown -R 2000:2000 /tofu \
  && chmod -R g+rwX /tofu

# Crossplane overrides this with runAsUser 2000; see the chown above.
# https://github.com/crossplane/crossplane/blob/v1.3.0/internal/controller/pkg/revision/deployment.go#L32
USER 65532
ENTRYPOINT ["crossplane-opentofu-provider"]
