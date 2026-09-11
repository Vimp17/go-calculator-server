package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

/*
#cgo LDFLAGS: -ldl
#include <stdlib.h>
#include <dlfcn.h>
#include <stdint.h>

typedef int64_t (*calc_func)(int64_t, int64_t);

void* load_lib(const char* path) {
    return dlopen(path, RTLD_NOW | RTLD_LOCAL);
}

void* get_sym(void* handle, const char* name) {
    return dlsym(handle, name);
}

void close_lib(void* handle) {
    dlclose(handle);
}

// C-обёртка для вызова указателя на функцию
int64_t call_calc(void* func, int64_t a, int64_t b) {
    return ((calc_func)func)(a, b);
}
*/
import "C"

var (
	addFuncPtr unsafe.Pointer
	subFuncPtr unsafe.Pointer
	cHandle    unsafe.Pointer
	rHandle    unsafe.Pointer

	sumValue int64
	subValue int64

	reqCount   uint64
	rpsHistory [60]uint64
	rpsIdx     int
	rpsMu      sync.Mutex

	cLats      [100000]uint64
	cLatIdx    uint64
	rustLats   [100000]uint64
	rustLatIdx uint64
)

func loadLibraries(cPath, rustPath string) error {
	cPathC := C.CString(cPath)
	defer C.free(unsafe.Pointer(cPathC))

	cHandle = C.load_lib(cPathC)
	if cHandle == nil {
		return fmt.Errorf("failed to load C library: %s", cPath)
	}

	addSymName := C.CString("add")
	defer C.free(unsafe.Pointer(addSymName))
	addFuncPtr = C.get_sym(cHandle, addSymName)
	if addFuncPtr == nil {
		return fmt.Errorf("symbol 'add' not found in C library")
	}

	rustPathC := C.CString(rustPath)
	defer C.free(unsafe.Pointer(rustPathC))

	rHandle = C.load_lib(rustPathC)
	if rHandle == nil {
		return fmt.Errorf("failed to load Rust library: %s", rustPath)
	}

	subSymName := C.CString("sub")
	defer C.free(unsafe.Pointer(subSymName))
	subFuncPtr = C.get_sym(rHandle, subSymName)
	if subFuncPtr == nil {
		return fmt.Errorf("symbol 'sub' not found in Rust library")
	}

	return nil
}

func calcHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	numStr := r.URL.Query().Get("num")
	if numStr == "" {
		http.Error(w, "missing 'num' query parameter", http.StatusBadRequest)
		return
	}

	num, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		http.Error(w, "'num' must be an integer", http.StatusBadRequest)
		return
	}

	// Lock-free CAS для C функции
	for {
		oldSum := atomic.LoadInt64(&sumValue)
		startC := time.Now().UnixNano()
		// Вызов через C-обёртку
		newSum := int64(C.call_calc(addFuncPtr, C.int64_t(oldSum), C.int64_t(num)))
		endC := time.Now().UnixNano()

		if atomic.CompareAndSwapInt64(&sumValue, oldSum, newSum) {
			idx := atomic.AddUint64(&cLatIdx, 1) - 1
			cLats[idx%100000] = uint64(endC - startC)
			break
		}
		runtime.Gosched()
	}

	// Lock-free CAS для Rust функции
	for {
		oldSub := atomic.LoadInt64(&subValue)
		startRust := time.Now().UnixNano()
		// Вызов через C-обёртку
		newSub := int64(C.call_calc(subFuncPtr, C.int64_t(oldSub), C.int64_t(num)))
		endRust := time.Now().UnixNano()

		if atomic.CompareAndSwapInt64(&subValue, oldSub, newSub) {
			idx := atomic.AddUint64(&rustLatIdx, 1) - 1
			rustLats[idx%100000] = uint64(endRust - startRust)
			break
		}
		runtime.Gosched()
	}

	atomic.AddUint64(&reqCount, 1)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	var sb strings.Builder

	sb.WriteString("# HELP rps Requests per second for each of the last 60 seconds\n")
	sb.WriteString("# TYPE rps gauge\n")
	rpsMu.Lock()
	hist := rpsHistory
	idx := rpsIdx
	rpsMu.Unlock()

	for i := 0; i < 60; i++ {
		realIdx := (idx + i) % 60
		fmt.Fprintf(&sb, "rps{sec=\"%d\"} %d\n", i, hist[realIdx])
	}

	sb.WriteString("# HELP c_func_latency_ns C function execution latency in nanoseconds\n")
	sb.WriteString("# TYPE c_func_latency_ns gauge\n")
	cCount := atomic.LoadUint64(&cLatIdx)
	if cCount > 100000 {
		cCount = 100000
	}
	if cCount > 0 {
		cLatsCopy := make([]uint64, cCount)
		copy(cLatsCopy, cLats[:cCount])
		sort.Slice(cLatsCopy, func(i, j int) bool { return cLatsCopy[i] < cLatsCopy[j] })

		p95Idx := int(math.Floor(float64(cCount) * 0.95))
		p99Idx := int(math.Floor(float64(cCount) * 0.99))
		if p95Idx >= int(cCount) {
			p95Idx = int(cCount) - 1
		}
		if p99Idx >= int(cCount) {
			p99Idx = int(cCount) - 1
		}

		fmt.Fprintf(&sb, "c_func_latency_p95_ns %d\n", cLatsCopy[p95Idx])
		fmt.Fprintf(&sb, "c_func_latency_p99_ns %d\n", cLatsCopy[p99Idx])
	} else {
		sb.WriteString("c_func_latency_p95_ns 0\n")
		sb.WriteString("c_func_latency_p99_ns 0\n")
	}

	sb.WriteString("# HELP rust_func_latency_ns Rust function execution latency in nanoseconds\n")
	sb.WriteString("# TYPE rust_func_latency_ns gauge\n")
	rustCount := atomic.LoadUint64(&rustLatIdx)
	if rustCount > 100000 {
		rustCount = 100000
	}
	if rustCount > 0 {
		rustLatsCopy := make([]uint64, rustCount)
		copy(rustLatsCopy, rustLats[:rustCount])
		sort.Slice(rustLatsCopy, func(i, j int) bool { return rustLatsCopy[i] < rustLatsCopy[j] })

		p95Idx := int(math.Floor(float64(rustCount) * 0.95))
		p99Idx := int(math.Floor(float64(rustCount) * 0.99))
		if p95Idx >= int(rustCount) {
			p95Idx = int(rustCount) - 1
		}
		if p99Idx >= int(rustCount) {
			p99Idx = int(rustCount) - 1
		}

		fmt.Fprintf(&sb, "rust_func_latency_p95_ns %d\n", rustLatsCopy[p95Idx])
		fmt.Fprintf(&sb, "rust_func_latency_p99_ns %d\n", rustLatsCopy[p99Idx])
	} else {
		sb.WriteString("rust_func_latency_p95_ns 0\n")
		sb.WriteString("rust_func_latency_p99_ns 0\n")
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(sb.String()))
}

func main() {
	host := flag.String("host", "0.0.0.0", "host to bind")
	port := flag.Int("port", 8080, "port to bind")
	cLib := flag.String("c-lib", "libcalculator.so", "path to C shared library")
	rustLib := flag.String("rust-lib", "libcalculator_rust.so", "path to Rust shared library")
	interval := flag.Float64("interval", 5.0, "seconds between periodic sum/sub reports")
	flag.Parse()

	if err := loadLibraries(*cLib, *rustLib); err != nil {
		log.Fatalf("Failed to load native libraries: %v\nDid you run build.sh first?", err)
	}
	defer C.close_lib(cHandle)
	defer C.close_lib(rHandle)

	mux := http.NewServeMux()
	mux.HandleFunc("/calc", calcHandler)
	mux.HandleFunc("/metrics", metricsHandler)

	addr := fmt.Sprintf("%s:%d", *host, *port)
	server := &http.Server{Addr: addr, Handler: mux}

	stopChan := make(chan struct{})

	go func() {
		ticker := time.NewTicker(time.Duration(*interval * float64(time.Second)))
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s := atomic.LoadInt64(&sumValue)
				sub := atomic.LoadInt64(&subValue)
				fmt.Printf("[periodic] sum=%d sub=%d\n", s, sub)
			case <-stopChan:
				return
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				count := atomic.SwapUint64(&reqCount, 0)
				rpsMu.Lock()
				rpsHistory[rpsIdx] = count
				rpsIdx = (rpsIdx + 1) % 60
				rpsMu.Unlock()
			case <-stopChan:
				return
			}
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nSIGINT received, shutting down...")
		s := atomic.LoadInt64(&sumValue)
		sub := atomic.LoadInt64(&subValue)
		fmt.Printf("[final] sum=%d sub=%d\n", s, sub)
		close(stopChan)
		server.Shutdown(context.Background())
	}()

	fmt.Printf("Calculator server listening on %s\n", addr)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("HTTP server error: %v", err)
	}
}
