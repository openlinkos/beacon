# The linkos-status image.
#
# The binary is compiled outside the image and placed at <platform>/linkos-status in the build context (GoReleaser
# does this when releasing). This file only copies files, and the final stage has no RUN, so building the arm64
# and arm/v7 images needs no QEMU. The base images are pinned by digest and updated by Dependabot.

# The data directory belongs to nonroot (65532): a new named volume takes this ownership.
FROM --platform=$BUILDPLATFORM docker.io/library/busybox:1.38@sha256:fd7dc98638c8e305f4dc34e979f1c0fdfdcaeb0fbf8fcff77ae834b6da3d7e6e AS files
RUN mkdir -p /out/var/lib/linkos-status /out/etc/linkos-status/catalog \
 && chown 65532:65532 /out/var/lib/linkos-status \
 && chmod 0700 /out/var/lib/linkos-status

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
ARG TARGETPLATFORM
COPY --from=files /out/ /
COPY --chmod=0755 $TARGETPLATFORM/linkos-status /usr/local/bin/linkos-status
USER 65532:65532
EXPOSE 17001
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/linkos-status", "probe"]
ENTRYPOINT ["/usr/local/bin/linkos-status"]
CMD ["serve"]
