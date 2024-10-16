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
RUN go build -o main .

# Final Stage
FROM alpine:latest

WORKDIR /app

# Copy the pre-built binary file from the previous stage
COPY --from=builder /app/main .

# Copy the static files and data directory
COPY --from=builder /app/static ./static
COPY --from=builder /app/data ./data

# Expose port 3000 to the outside world
EXPOSE 3000

# Command to run the executable
CMD ["./main"]
