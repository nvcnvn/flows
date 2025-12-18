# Build a single stress binary that can run in worker or generator mode.
# Use a stable Go image and allow auto toolchain download if go.mod requires newer.
FROM golang:1.25 AS build

ENV GOTOOLCHAIN=auto

WORKDIR /src
COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 go build -o /out/stress ./examples/stress

FROM gcr.io/distroless/static-debian12
WORKDIR /
COPY --from=build /out/stress /stress
ENTRYPOINT ["/stress"]
