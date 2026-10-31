# The Beacon image (linkos-beacon, and linkos-probe).
#
# The binaries are compiled outside the image and placed at <platform>/linkos-beacon and <platform>/linkos-probe
# in the build context (GoReleaser does this when releasing). linkos-probe is the probe agent; the image carries
# it so that the monitoring host can run its own probe from the same image. This file only copies files, and the
# final stage has no RUN, so building the arm64 image needs no QEMU. The base images are pinned by digest and
# updated by Dependabot.

# The data directory belongs to nonroot (65532): a new named volume takes this ownership.
FROM --platform=$BUILDPLATFORM docker.io/library/busybox:1.38@sha256:fd7dc98638c8e305f4dc34e979f1c0fdfdcaeb0fbf8fcff77ae834b6da3d7e6e AS files
RUN mkdir -p /out/var/lib/linkos-beacon /out/etc/linkos-beacon/catalog /out/etc/linkos-beacon/targets \
 && chown 65532:65532 /out/var/lib/linkos-beacon \
 && chmod 0700 /out/var/lib/linkos-beacon

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
ARG TARGETPLATFORM
COPY --from=files /out/ /
COPY --chmod=0755 $TARGETPLATFORM/linkos-beacon /usr/local/bin/linkos-beacon
COPY --chmod=0755 $TARGETPLATFORM/linkos-probe /usr/local/bin/linkos-probe
USER 65532:65532
EXPOSE 17001 17006
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/linkos-beacon", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/linkos-beacon"]
CMD ["serve"]
