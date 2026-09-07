package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

type inputLine struct {
	text   string
	number int
	err    error
}
type result struct {
	label, saved string
	size         int64
	err          error
	inputError   bool
}

// The reader goroutine lets cancellation finish even when stdin is waiting for
// another line. An embedding caller owns and closes any blocking input reader.
func readLines(ctx context.Context, in io.Reader, jobs chan<- inputLine) {
	defer close(jobs)
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		select {
		case jobs <- inputLine{text: text, number: line}:
		case <-ctx.Done():
			return
		}
	}
	if err := scanner.Err(); err != nil {
		select {
		case jobs <- inputLine{number: line + 1, err: fmt.Errorf("read input: %w", err)}:
		case <-ctx.Done():
		}
	}
}

func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	cfg, done, err := parseConfig(args, out)
	if err != nil {
		fmt.Fprintf(errOut, "fget: %v\nUse --help for usage.\n", err)
		return 2
	}
	if done {
		return 0
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if ctx.Err() != nil {
		fmt.Fprintln(errOut, "fget: canceled")
		return 130
	}
	if err := os.MkdirAll(cfg.outputRoot(), 0755); err != nil {
		fmt.Fprintf(errOut, "fget: create output directory: %v\n", err)
		return 1
	}
	root, err := os.OpenRoot(cfg.outputRoot())
	if err != nil {
		fmt.Fprintf(errOut, "fget: open output directory: %v\n", err)
		return 1
	}
	defer root.Close()
	client := newClient(cfg)
	defer client.CloseIdleConnections()
	if cfg.url != "" {
		in = strings.NewReader(cfg.url)
	}
	jobs := make(chan inputLine, cfg.workers)
	results := make(chan result, cfg.workers)
	go readLines(ctx, in, jobs)
	var wg sync.WaitGroup
	for range cfg.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case line, ok := <-jobs:
					if !ok || ctx.Err() != nil {
						return
					}
					r := result{label: fmt.Sprintf("input line %d", line.number), err: line.err, inputError: line.err != nil}
					if line.err == nil {
						u, err := parseURL(line.text)
						if err != nil {
							r.err, r.inputError = err, true
						} else {
							r.label = u.Scheme + "://" + u.Host + u.EscapedPath()
							r.saved, r.size, r.err = download(ctx, client, root, cfg, u)
						}
					}
					results <- r
				}
			}
		}()
	}
	go func() { wg.Wait(); close(results) }()
	var succeeded, failed int
	var bytes int64
	code := 0
	for r := range results {
		if r.err != nil {
			failed++
			fmt.Fprintf(errOut, "fget: %s: %v\n", r.label, r.err)
			if code == 0 {
				code = 1
			}
			if r.inputError {
				code = 2
			}
		} else {
			succeeded++
			bytes += r.size
			if cfg.verbose {
				fmt.Fprintf(errOut, "Saved %s (%d bytes)\n", r.saved, r.size)
			}
		}
	}
	fmt.Fprintf(errOut, "Downloaded: %d; failed: %d; bytes: %d\n", succeeded, failed, bytes)
	if ctx.Err() != nil {
		fmt.Fprintln(errOut, "fget: canceled")
		return 130
	}
	if succeeded+failed == 0 {
		fmt.Fprintln(errOut, "fget: no URLs supplied; use --url or stdin")
		return 2
	}
	return code
}
