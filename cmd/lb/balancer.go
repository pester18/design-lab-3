package main

import (
	"context"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/KPI-Labs/design-lab-3/httptools"
	"github.com/KPI-Labs/design-lab-3/signal"
)

var (
	port         = flag.Int("port", 8090, "load balancer port")
	timeoutSec   = flag.Int("timeout-sec", 3, "request timeout time in seconds")
	https        = flag.Bool("https", false, "whether backends support HTTPs")
	traceEnabled = flag.Bool("trace", false, "whether to include tracing information into responses")
)

type ServersPool struct {
	pool  []string
	mutex sync.Mutex
}

var (
	timeout     = time.Duration(*timeoutSec) * time.Second
	serversPool = ServersPool{pool: []string{
		"server1:8080",
		"server2:8080",
		"server3:8080",
	},
	}
)

func hash(data string) uint32 {
	hasher := fnv.New32a()
	hasher.Write([]byte(data))
	return hasher.Sum32()
}

func scheme() string {
	if *https {
		return "https"
	}
	return "http"
}

func health(dst string) bool {
	ctx, _ := context.WithTimeout(context.Background(), timeout)
	req, _ := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s://%s/health", scheme(), dst), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return true
}

func forward(dst string, rw http.ResponseWriter, r *http.Request) error {
	ctx, _ := context.WithTimeout(r.Context(), timeout)
	fwdRequest := r.Clone(ctx)
	fwdRequest.RequestURI = ""
	fwdRequest.URL.Host = dst
	fwdRequest.URL.Scheme = scheme()
	fwdRequest.Host = dst

	resp, err := http.DefaultClient.Do(fwdRequest)
	if err == nil {
		for k, values := range resp.Header {
			for _, value := range values {
				rw.Header().Add(k, value)
			}
		}
		if *traceEnabled {
			rw.Header().Set("lb-from", dst)
		}
		log.Println("fwd", resp.StatusCode, resp.Request.URL)
		rw.WriteHeader(resp.StatusCode)
		defer resp.Body.Close()
		_, err := io.Copy(rw, resp.Body)
		if err != nil {
			log.Printf("Failed to write response: %s", err)
		}
		return nil
	} else {
		log.Printf("Failed to get response from %s: %s", dst, err)
		rw.WriteHeader(http.StatusServiceUnavailable)
		return err
	}
}

func find(a []string, x string) int {
	for i, n := range a {
		if x == n {
			return i
		}
	}
	return -1
}

func getServerByURL(url string) string {
	serversPool.mutex.Lock()
	serverIndex := int(hash(url)) % len(serversPool.pool)
	server := serversPool.pool[serverIndex]
	serversPool.mutex.Unlock()
	return server
}

func main() {
	flag.Parse()

	for _, server := range serversPool.pool {
		server := server
		go func() {
			for range time.Tick(10 * time.Second) {
				isAlive := health(server)
				serversPool.mutex.Lock()
				index := find(serversPool.pool, server)
				if isAlive && index == -1 {
					serversPool.pool = append(serversPool.pool, server)
				}
				if !isAlive && index != -1 {
					lastIndex := len(serversPool.pool) - 1
					serversPool.pool[index] = serversPool.pool[lastIndex]
					serversPool.pool[lastIndex] = ""
				}
				serversPool.mutex.Unlock()
				log.Println(server, isAlive)
			}
		}()
	}

	frontend := httptools.CreateServer(*port, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		server := getServerByURL(r.URL.Path)
		forward(server, rw, r)
	}))

	log.Println("Starting load balancer...")
	log.Printf("Tracing support enabled: %t", *traceEnabled)
	frontend.Start()
	signal.WaitForTerminationSignal()
}
