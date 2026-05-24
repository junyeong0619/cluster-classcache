# v0.7 operator image.
#
# Build context = repo root (build.sh handles this).
#
# Final image is gcr.io/distroless/static:nonroot — operator is a pure Go
# binary that talks to the k8s API; no JVM, no shell, no libc needed.
# Footprint: ~15 MB.

FROM golang:1.22 AS gobuild
WORKDIR /src
COPY modules/operator/go.mod modules/operator/go.sum* ./
RUN go mod download
COPY modules/operator/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -trimpath -o /out/operator ./cmd

FROM gcr.io/distroless/static:nonroot
COPY --from=gobuild --chown=nonroot:nonroot /out/operator /operator
USER nonroot
ENTRYPOINT ["/operator"]
