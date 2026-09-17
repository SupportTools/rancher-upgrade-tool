# Build Stage
FROM golang:1.22.4-alpine3.20  AS builder

# Set the Current Working Directory inside the container
WORKDIR /app

# Copy go.mod and go.sum files to the workspace
COPY go.mod go.sum ./

# Download all dependencies
RUN go mod download

# Copy the source code and required directories to the Working Directory inside the container
COPY . .

# Build the Go app
# CGO_ENABLED=0 so the binary is static and does not depend on the runtime
# image's musl. -trimpath strips local filesystem paths, which otherwise make
# the binary differ between machines for identical source.
RUN CGO_ENABLED=0 go build -trimpath -o main .

# Final Stage
# Pinned to match the builder's alpine3.20. Previously `alpine:latest`, which
# combined with the nightly cron meant production ran a different base image
# most mornings for identical source.
#
# NOTE: a tag pin narrows drift, it does not eliminate it -- alpine:3.20 still
# moves across 3.20.x patch releases. Pinning by sha256 digest is the stronger
# form and needs a registry lookup to establish.
FROM alpine:3.20

WORKDIR /app

# Copy the pre-built binary file from the previous stage
COPY --from=builder /app/main .

# Copy the static files and data directory
COPY --from=builder /app/static ./static
# Copy ONLY the live catalog, not the whole data directory.
#
# data/upgrade-paths.json is the superseded pre-rewrite dataset. It stays in the
# repo because 63 of its 70 entries are NOT re-derivable from upstream (endoflife
# publishes only the latest patch per cycle, and SUSE retires old per-version
# matrix pages), but it has no business in the image: dead weight, and a second
# compatibility dataset sitting next to the live one invites someone to load the
# wrong file.
COPY --from=builder /app/data/catalog.json ./data/catalog.json

# Expose port 3000 to the outside world
EXPOSE 3000

# Command to run the executable
CMD ["./main"]
