# The Beacon image (beacon, and beacon-probe).
#
# The binaries are compiled outside the image and placed at <platform>/beacon and <platform>/beacon-probe
# in the build context (GoReleaser does this when releasing). beacon-probe is the probe agent; the image carries
# it so that the monitoring host can run its own probe from the same image. This file only copies files, and the
# final stage has no RUN, so building the arm64 image needs no QEMU. The base images are pinned by digest and
# updated by Dependabot.

# The data directory belongs to nonroot (65532): a new named volume takes this ownership.
FROM --platform=$BUILDPLATFORM docker.io/library/busybox:1.38@sha256:a46257e06c82ad85242d0d38077d0a52d9896389ca60a1a59d3463db586e5fa4 AS files
RUN mkdir -p /out/var/lib/beacon /out/etc/beacon/catalog /out/etc/beacon/targets \
 && chown 65532:65532 /out/var/lib/beacon \
 && chmod 0700 /out/var/lib/beacon

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
ARG TARGETPLATFORM
COPY --from=files /out/ /
COPY --chmod=0755 $TARGETPLATFORM/beacon /usr/local/bin/beacon
COPY --chmod=0755 $TARGETPLATFORM/beacon-probe /usr/local/bin/beacon-probe
USER 65532:65532
EXPOSE 17001 17002 17006
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/beacon", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/beacon"]
CMD ["serve"]
