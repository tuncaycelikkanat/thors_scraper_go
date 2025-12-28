package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/gocolly/colly/v2"
	"golang.org/x/net/proxy"
)

const (
	torProxy      = "127.0.0.1:9050"
	maxConcurrent = 5
)

// log format

func logf(level, target, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	log.Printf("[%s] [%s] %s", level, target, msg)
}

func createOutputDirectory(raw string) string {
	name := strings.ReplaceAll(raw, "http://", "")
	name = strings.ReplaceAll(name, "https://", "")
	name = strings.ReplaceAll(name, "/", "_")
	return name
}

func readTargets(filename string) ([]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var targets []string
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())

		if raw == "" {
			continue
		}

		if strings.ContainsAny(raw, " \t") {
			logf("WARN", "-", "Invalid URL (whitespace): %s", raw)
			continue
		}

		_, err := url.ParseRequestURI(raw)
		if err != nil {
			logf("WARN", "-", "Invalid URL format: %s", raw)
			continue
		}

		targets = append(targets, raw)
	}

	return targets, scanner.Err()
}

func takeScreenshot(targetURL string) error {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ProxyServer("socks5://127.0.0.1:9050"),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
	)

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var buf []byte
	err := chromedp.Run(
		ctx,
		chromedp.Navigate(targetURL),
		chromedp.Sleep(10*time.Second),
		chromedp.FullScreenshot(&buf, 90),
	)
	if err != nil {
		return err
	}

	filename := "outputs/screenshots/" + createOutputDirectory(targetURL) + ".png"
	return os.WriteFile(filename, buf, 0644)
}

func fetchHTMLWithColly(targetURL, outputPath string) error {

	targetID := createOutputDirectory(targetURL)

	dialer, err := proxy.SOCKS5("tcp", torProxy, nil, proxy.Direct)
	if err != nil {
		return err
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		},
	}

	c := colly.NewCollector(
		colly.UserAgent(
			"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "+
				"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		),
		colly.AllowURLRevisit(),
	)

	c.WithTransport(transport)
	c.SetRequestTimeout(30 * time.Second)

	c.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Delay:       2 * time.Second,
		RandomDelay: 1 * time.Second,
	})

	c.OnRequest(func(r *colly.Request) {
		logf("REQ", targetID, "→ Request: %s", r.URL)
	})

	c.OnResponse(func(r *colly.Response) {
		logf("RESP", targetID, "← Status: %d", r.StatusCode)
		logf("RESP", targetID, "← Server: %s", r.Headers.Get("Server"))

		if err := os.WriteFile(outputPath, r.Body, 0644); err != nil {
			logf("ERR", targetID, "HTML save failed")
			return
		}

		logf("OK", targetID, "HTML saved")

		if err := takeScreenshot(targetURL); err != nil {
			logf("ERR", targetID, "Screenshot failed")
		} else {
			logf("OK", targetID, "Screenshot saved")
		}
	})

	c.OnError(func(r *colly.Response, err error) {
		if r != nil {
			logf("ERR", targetID, "Request error: %v", err)
		}
	})

	return c.Visit(targetURL)
}

func main() {
	os.MkdirAll("outputs/html", 0755)
	os.MkdirAll("outputs/screenshots", 0755)
	os.MkdirAll("outputs/logs", 0755)

	logFile, _ := os.Create("outputs/logs/scan_report.log")
	log.SetOutput(logFile)
	log.SetFlags(log.Ldate | log.Ltime)

	targets, err := readTargets("targets.yaml")
	if err != nil {
		log.Fatal(err)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrent)

	for _, target := range targets {
		wg.Add(1)

		go func(t string) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			targetID := createOutputDirectory(t)
			logf("INFO", targetID, "Scanning")

			htmlPath := "outputs/html/" + targetID + ".html"
			if err := fetchHTMLWithColly(t, htmlPath); err != nil {
				logf("ERR", targetID, "Fetch failed: %v", err)
			}
		}(target)
	}

	wg.Wait()
	logf("INFO", "-", "ALL TASKS COMPLETED")
}
