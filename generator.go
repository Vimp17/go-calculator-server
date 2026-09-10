package main

import (
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	url := flag.String("url", "http://localhost:8080/calc", "calculator endpoint")
	threads := flag.Int("n", 10, "number of worker goroutines")
	interval := flag.Float64("interval", 0.1, "pause between requests per thread, in seconds (0 = as fast as possible)")
	timeout := flag.Float64("timeout", 5.0, "HTTP request timeout, seconds")
	flag.Parse()

	client := &http.Client{
		Timeout: time.Duration(*timeout * float64(time.Second)),
	}

	var okCount, errCount uint64
	stopChan := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < *threads; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-stopChan:
					return
				default:
					num := rand.Intn(201) - 100 // -100 to 100
					reqUrl := fmt.Sprintf("%s?num=%d", *url, num)

					req, err := http.NewRequest(http.MethodPost, reqUrl, nil)
					if err != nil {
						atomic.AddUint64(&errCount, 1)
						continue
					}

					resp, err := client.Do(req)
					if err != nil {
						atomic.AddUint64(&errCount, 1)
					} else {
						resp.Body.Close()
						atomic.AddUint64(&okCount, 1)
					}

					if *interval > 0 {
						time.Sleep(time.Duration(*interval * float64(time.Second)))
					}
				}
			}
		}(i)
	}

	fmt.Printf("Generator started: %d goroutines -> %s\n", *threads, *url)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nSIGINT received, stopping generator...")
	close(stopChan)
	wg.Wait()

	fmt.Printf("Total requests: ok=%d errors=%d\n", atomic.LoadUint64(&okCount), atomic.LoadUint64(&errCount))
}
