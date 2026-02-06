package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var webFS embed.FS

type CPUTimes struct{ User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal, Guest, GuestNice uint64 }

type NetStat struct {
	Name     string `json:"name"`
	RxBytes  uint64 `json:"rx_bytes"`
	TxBytes  uint64 `json:"tx_bytes"`
	RxPkts   uint64 `json:"rx_packets"`
	TxPkts   uint64 `json:"tx_packets"`
	OperUp   bool   `json:"oper_up"`
	AddrIPv4 string `json:"addr_ipv4,omitempty"`
}

type Temp struct {
	Sensor string  `json:"sensor"`
	C      float64 `json:"celsius"`
}

type Metrics struct {
	Timestamp  time.Time  `json:"timestamp"`
	Hostname   string     `json:"hostname"`
	OS         string     `json:"os"`
	Kernel     string     `json:"kernel"`
	UptimeSec  uint64     `json:"uptime_sec"`
	Load1      float64    `json:"load1"`
	Load5      float64    `json:"load5"`
	Load15     float64    `json:"load15"`
	CPUPercent float64    `json:"cpu_percent"`
	CPUCores   int        `json:"cpu_cores"`
	MemTotalB  uint64     `json:"mem_total_bytes"`
	MemAvailB  uint64     `json:"mem_available_bytes"`
	SwapTotalB uint64     `json:"swap_total_bytes"`
	SwapFreeB  uint64     `json:"swap_free_bytes"`
	Net        []NetStat  `json:"net"`
	Temps      []Temp     `json:"temps"`
	LastError  string     `json:"last_error,omitempty"`
}

var (
	mtx         sync.RWMutex
	current     Metrics
	history     []Metrics
	outDir      = "/var/lib/sysdash"
	outFile     = "metrics.json"
	sampleEvery = 2 * time.Second
)

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func parseCPUTimes() (CPUTimes, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return CPUTimes{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 0 && fields[0] == "cpu" {
			// cpu  user nice system idle iowait irq softirq steal guest guest_nice
			get := func(i int) uint64 {
				if i >= len(fields) {
					return 0
				}
				v, _ := strconv.ParseUint(fields[i], 10, 64)
				return v
			}
			return CPUTimes{
				User: get(1), Nice: get(2), System: get(3), Idle: get(4),
				IOWait: get(5), IRQ: get(6), SoftIRQ: get(7), Steal: get(8),
				Guest: get(9), GuestNice: get(10),
			}, nil
		}
	}
	return CPUTimes{}, errors.New("cpu line not found")
}

func cpuPercent(prev, cur CPUTimes) float64 {
	idlePrev := prev.Idle + prev.IOWait
	idleCur := cur.Idle + cur.IOWait
	nonPrev := prev.User + prev.Nice + prev.System + prev.IRQ + prev.SoftIRQ + prev.Steal
	nonCur := cur.User + cur.Nice + cur.System + cur.IRQ + cur.SoftIRQ + cur.Steal
	idleDelta := float64(idleCur - idlePrev)
	nonDelta := float64(nonCur - nonPrev)
	total := idleDelta + nonDelta
	if total <= 0 {
		return 0
	}
	return math.Max(0, math.Min(100, (nonDelta/total)*100))
}

func readMem() (total, avail, swapT, swapF uint64, err error) {
	f, e := os.Open("/proc/meminfo")
	if e != nil {
		err = e
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var key, unit string
		var val uint64
		fmt.Sscanf(sc.Text(), "%s %d %s", &key, &val, &unit)
		switch strings.TrimSuffix(key, ":") {
		case "MemTotal":
			total = val * 1024
		case "MemAvailable":
			avail = val * 1024
		case "SwapTotal":
			swapT = val * 1024
		case "SwapFree":
			swapF = val * 1024
		}
	}
	return
}

func readLoad() (l1, l5, l15 float64, err error) {
	s, e := readFile("/proc/loadavg")
	if e != nil {
		return 0, 0, 0, e
	}
	parts := strings.Fields(s)
	if len(parts) < 3 {
		return 0, 0, 0, errors.New("bad loadavg")
	}
	l1, _ = strconv.ParseFloat(parts[0], 64)
	l5, _ = strconv.ParseFloat(parts[1], 64)
	l15, _ = strconv.ParseFloat(parts[2], 64)
	return
}

func readUptime() (uint64, error) {
	s, e := readFile("/proc/uptime")
	if e != nil {
		return 0, e
	}
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0, errors.New("bad uptime")
	}
	up, _ := strconv.ParseFloat(f[0], 64)
	return uint64(up), nil
}

func readKernel() string {
	uts := syscall.Utsname{}
	if err := syscall.Uname(&uts); err != nil {
		return ""
	}
	toStr := func(a [65]int8) string {
		var b strings.Builder
		for _, c := range a {
			if c == 0 {
				break
			}
			b.WriteByte(byte(c))
		}
		return b.String()
	}
	return fmt.Sprintf("%s %s", toStr(uts.Sysname), toStr(uts.Release))
}

// tiny helper
func readUint(path string) uint64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return v
}

func readNet() []NetStat {
	var out []NetStat

	ifaces, err := net.Interfaces()
	if err != nil {
		log.Printf("[readNet] net.Interfaces error: %v", err)
		return out
	}

	for _, ifc := range ifaces {
		name := ifc.Name

		// operstate from sysfs, with safe fallback to net.Flags
		opPath := filepath.Join("/sys/class/net", name, "operstate")
		state := "unknown"
		if b, err := os.ReadFile(opPath); err == nil {
			state = strings.TrimSpace(string(b))
		}
		operUp := state == "up"
		if state == "unknown" { // some drivers report unknown, use flags as hint
			operUp = ifc.Flags&net.FlagUp != 0
		}

		// stats from sysfs
		base := filepath.Join("/sys/class/net", name, "statistics")
		rxB := readUint(filepath.Join(base, "rx_bytes"))
		rxP := readUint(filepath.Join(base, "rx_packets"))
		txB := readUint(filepath.Join(base, "tx_bytes"))
		txP := readUint(filepath.Join(base, "tx_packets"))

		// IPv4 address
		var ipv4 string
		if addrs, _ := ifc.Addrs(); addrs != nil {
			for _, a := range addrs {
				if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
					ipv4 = ipnet.IP.String()
					break
				}
			}
		}

		out = append(out, NetStat{
			Name:     name,
			RxBytes:  rxB,
			TxBytes:  txB,
			RxPkts:   rxP,
			TxPkts:   txP,
			OperUp:   operUp,
			AddrIPv4: ipv4,
		})
	}

	return out
}

func readTemps() []Temp {
	var out []Temp
	_ = filepath.WalkDir("/sys/class/thermal", func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || !strings.HasPrefix(filepath.Base(path), "thermal_zone") {
			return nil
		}
		typePath := filepath.Join(path, "type")
		tempPath := filepath.Join(path, "temp")
		typ, e1 := os.ReadFile(typePath)
		val, e2 := os.ReadFile(tempPath)
		if e1 == nil && e2 == nil {
			raw := strings.TrimSpace(string(val))
			f, _ := strconv.ParseFloat(raw, 64)
			// many drivers report millidegC; fallback if it looks like plain C
			if f > 200 {
				f = f / 1000.0
			}
			out = append(out, Temp{Sensor: strings.TrimSpace(string(typ)), C: f})
		}
		return nil
	})
	return out
}

func ensureDir(p string) {
	if err := os.MkdirAll(p, 0o755); err != nil {
		log.Fatalf("mkdir %s: %v", p, err)
	}
}

func writeJSON(m Metrics) {
	ensureDir(outDir)
	path := filepath.Join(outDir, outFile)
	tmp := path + ".tmp"
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		_ = os.Rename(tmp, path)
	}
}

func collectLoop() {
	host, _ := os.Hostname()
	kernel := readKernel()
	cores := runtime.NumCPU()
	prev, _ := parseCPUTimes()
	for {
		start := time.Now()
		cur, errCT := parseCPUTimes()
		memT, memA, swT, swF, errM := readMem()
		l1, l5, l15, errL := readLoad()
		up, errU := readUptime()
		net := readNet()
		temps := readTemps()

		errs := []string{}
		if errCT != nil {
			errs = append(errs, "cpustat:"+errCT.Error())
		}
		if errM != nil {
			errs = append(errs, "meminfo:"+errM.Error())
		}
		if errL != nil {
			errs = append(errs, "loadavg:"+errL.Error())
		}
		if errU != nil {
			errs = append(errs, "uptime:"+errU.Error())
		}

		cpuPct := cpuPercent(prev, cur)
		prev = cur

		m := Metrics{
			Timestamp: time.Now(),
			Hostname:  host,
			OS:        runtime.GOOS + "/" + runtime.GOARCH,
			Kernel:    kernel,
			UptimeSec: up,
			Load1:     l1, Load5: l5, Load15: l15,
			CPUPercent: cpuPct,
			CPUCores:   cores,
			MemTotalB:  memT, MemAvailB: memA,
			SwapTotalB: swT, SwapFreeB: swF,
			Net:   net,
			Temps: temps,
		}
		if len(errs) > 0 {
			m.LastError = strings.Join(errs, "; ")
		}

		mtx.Lock()
		current = m
		history = append(history, m)
		if len(history) > 120 {
			history = history[1:]
		}
		mtx.Unlock()
		writeJSON(m)

		time.Sleep(time.Until(start.Add(sampleEvery)))
	}
}

func main() {
	if v := os.Getenv("SYSDASH_OUTDIR"); v != "" {
		outDir = v
	}
	if v := os.Getenv("SYSDASH_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			sampleEvery = d
		}
	}

	subFS, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("failed to prepare embedded FS: %v", err)
	}

	// New: support port flag/env
	port := flag.String("port", "", "Port to listen on (default 8080 or from SYSDASH_PORT)")
	flag.Parse()

	addr := ":8081" // default
	if envPort := os.Getenv("SYSDASH_PORT"); envPort != "" {
		addr = fmt.Sprintf(":%s", envPort)
	}
	if *port != "" {
		addr = fmt.Sprintf(":%s", *port)
	}

	go collectLoop()

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(subFS)))
	mux.HandleFunc("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
		mtx.RLock()
		m := current
		mtx.RUnlock()
		b, _ := json.MarshalIndent(m, "", "  ")
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	mux.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		mtx.RLock()
		h := make([]Metrics, len(history))
		copy(h, history)
		mtx.RUnlock()
		b, _ := json.MarshalIndent(h, "", "  ")
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	mux.HandleFunc("/api/metrics.json", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(outDir, outFile))
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	})

	log.Printf("sysdashd listening on %s, writing %s/%s (interval %s)", addr, outDir, outFile, sampleEvery)
	log.Fatal(http.ListenAndServe(addr, mux))
}
