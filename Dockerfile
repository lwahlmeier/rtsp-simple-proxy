FROM alpine:3.11

RUN apk update && \
    apk -Uuv add dumb-init ca-certificates bash && \
    rm /var/cache/apk/*
COPY run.sh /run.sh
RUN touch env.sh && chmod 755 /run.sh
COPY build/rtsp-proxy /rtsp-proxy
COPY sample.config.yaml /config.yaml

EXPOSE 554/tcp
EXPOSE 8050/udp
EXPOSE 8051/udp


ENTRYPOINT ["/run.sh"]
CMD ["/rtsp-proxy", "/config.yaml"]

