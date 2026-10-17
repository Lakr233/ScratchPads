# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/scratchpads . \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/Lakr233/ScratchPads" \
      org.opencontainers.image.description="Shared markdown pads for people and their agents" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/scratchpads /usr/local/bin/scratchpads
COPY --from=build --chown=65532:65532 /out/data /data
ENV SP_ADDR=:8443 SP_DATA=/data
EXPOSE 8443
VOLUME /data
USER 65532:65532
ENTRYPOINT ["scratchpads"]
CMD ["serve"]
