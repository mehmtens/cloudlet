FROM clamav/clamav:1.5.4-debian13-slim

USER root
ARG CLAMAV_STREAM_MAX_LENGTH=110M
RUN if grep -q '^StreamMaxLength ' /etc/clamav/clamd.conf; then \
      sed -i "s/^StreamMaxLength .*/StreamMaxLength ${CLAMAV_STREAM_MAX_LENGTH}/" /etc/clamav/clamd.conf; \
    else \
      printf '\nStreamMaxLength %s\n' "${CLAMAV_STREAM_MAX_LENGTH}" >> /etc/clamav/clamd.conf; \
    fi
