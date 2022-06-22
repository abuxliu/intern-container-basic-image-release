FROM scratch
ARG TARGETARCH
ADD openEuler-docker-rootfs.$TARGETARCH.tar.xz /
RUN ln -sf /usr/share/zoneinfo/UTC /etc/localtime
CMD ["bash"]
