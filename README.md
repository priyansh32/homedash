# SysDash

SysDash is a lightweight, real-time Linux system monitoring dashboard. It consists of a single binary backend written in Go and an embedded web frontend using vanilla JavaScript and Chart.js.

## Features

- **Real-time Metrics**: CPU, Memory, Load Averages, and Network Traffic.
- **Visualizations**: Live updating charts.
- **System Info**: Kernel version, Uptime, OS details.
- **Network Interfaces**: Status and IP addresses.
- **Hardware Temperatures**: CPU/Sensor thermal readings.
- **Single Binary**: The web assets are embedded, making deployment easy.

## Getting Started

### Prerequisites

- Go 1.16+ (to build)
- Linux environment (relies on `/proc` and `/sys` filesystems)

### Running Locally

```bash
# Run directly
go run main.go

# Or build and run
go build -o sysdash
./sysdash
```

Open your browser to `http://localhost:8081`.

### Building for Different Architectures

You can easily cross-compile the binary for different Linux architectures (e.g., Raspberry Pi, VPS):

```bash
# 64-bit Linux (AMD64)
GOOS=linux GOARCH=amd64 go build -o sysdash-amd64

# 64-bit ARM (Raspberry Pi 4, AWS Graviton)
GOOS=linux GOARCH=arm64 go build -o sysdash-arm64

# 32-bit ARM (Raspberry Pi Zero/2)
GOOS=linux GOARCH=arm go build -o sysdash-arm
```

### Embedded Frontend

This project uses Go's `embed` package to compile the `web/` directory (HTML, JS, CSS) directly into the binary. This makes deployment extremely simple: just copy the single executable file to your target machine. No external static files are needed at runtime.

### Configuration

You can configure SysDash using flags or environment variables:

| Env Variable       | Flag    | Default            | Description |
|--------------------|---------|--------------------|-------------|
| `SYSDASH_PORT`     | `-port` | `8081`             | Port to listen on |
| `SYSDASH_INTERVAL` | N/A     | `2s`               | Sampling interval |
| `SYSDASH_OUTDIR`   | N/A     | `/var/lib/sysdash` | Directory for JSON dump |

## License

MIT