// recorder is a debugging client for the Semantik API. It sends one
// request per invocation and prints the full HTTP request and response
// — including headers, body, status, and X-Request-Id — so failures
// can be correlated with server-side logs.
//
// Auth is read from NOETIVE_KEY_SECRET. Bearer tokens are redacted in
// the printed request dump.
//
// Usage:
//
//	recorder health
//	recorder lint   "MATCH DISTANCE(\"x\") WITHIN 0.5"
//	recorder publish [-ack durable] [-text "..."] [-vector 1,2,3,...]
//	recorder search  "MATCH DISTANCE(\"transformer\") WITHIN 0.6 LIMIT 5"
//	recorder subscribe [-wait 30s] "MATCH DISTANCE(\"x\") WITHIN 0.5"
//	recorder raw -method POST -path /v1/publish -body '{...}'
//
// All subcommands accept -url to override the base URL (default:
// production). -timeout caps the request (default 15s, except subscribe
// which uses -wait).
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"strconv"
	"strings"
	"time"

	"go.noetive.io/noetive-sdk-go/semantik"
)

const (
	defaultBaseURL = "https://semantik.noetive.io"
	defaultNS      = "global"
	defaultModel   = "Qwen3-Embedding-4B"
	defaultDims    = 1024
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "health":
		err = cmdHealth(args)
	case "lint":
		err = cmdLint(args)
	case "publish":
		err = cmdPublish(args)
	case "search":
		err = cmdSearch(args)
	case "subscribe":
		err = cmdSubscribe(args)
	case "raw":
		err = cmdRaw(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `recorder — Semantik API debugging client

  recorder health
  recorder lint <query>
  recorder publish [-ack stored|durable] [-text "..."|-vector 1,2,3]
  recorder search <query>
  recorder subscribe [-wait 30s] <query>
  recorder raw -method POST -path /v1/X -body '{...}'

Common flags (any subcommand):
  -url      base URL (default `+defaultBaseURL+`)
  -ns       namespace (default `+defaultNS+`)
  -model    model (default `+defaultModel+`)
  -dims     dimensions (default `+strconv.Itoa(defaultDims)+`)
  -timeout  request timeout (default 15s)

Auth: NOETIVE_KEY_SECRET env var (required).
`)
}

// commonFlags returns a fresh flag.FlagSet pre-populated with -url, -ns,
// -model, -dims, and -timeout flags, plus pointers to each value.
type common struct {
	url     string
	ns      string
	model   string
	dims    int
	timeout time.Duration
}

func registerCommon(fs *flag.FlagSet) *common {
	c := &common{}
	fs.StringVar(&c.url, "url", defaultBaseURL, "base URL")
	fs.StringVar(&c.ns, "ns", defaultNS, "namespace")
	fs.StringVar(&c.model, "model", defaultModel, "model")
	fs.IntVar(&c.dims, "dims", defaultDims, "dimensions")
	fs.DurationVar(&c.timeout, "timeout", 15*time.Second, "request timeout")
	return c
}

func cmdHealth(args []string) error {
	fs := flag.NewFlagSet("health", flag.ExitOnError)
	c := registerCommon(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return record("HEALTH", "GET", c.url+"/v1/health", "", c.timeout)
}

func cmdLint(args []string) error {
	fs := flag.NewFlagSet("lint", flag.ExitOnError)
	c := registerCommon(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("lint: missing <query> argument")
	}
	body := jsonObj(map[string]any{"query": fs.Arg(0)})
	return record("LINT", "POST", c.url+"/v1/lint", body, c.timeout)
}

func cmdPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	c := registerCommon(fs)
	ack := fs.String("ack", "stored", "ack mode (stored|durable)")
	text := fs.String("text", "", "text item to publish (mutually exclusive with -vector)")
	vector := fs.String("vector", "", "comma-separated float vector (mutually exclusive with -text)")
	idem := fs.String("idempotency-key", "", "idempotency key (default: random)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*text == "" && *vector == "") || (*text != "" && *vector != "") {
		return errors.New("publish: provide exactly one of -text or -vector")
	}
	item := map[string]any{}
	if *text != "" {
		item["text"] = *text
	} else {
		floats, err := parseFloats(*vector)
		if err != nil {
			return fmt.Errorf("publish: -vector: %w", err)
		}
		item["vector"] = floats
	}
	key := *idem
	if key == "" {
		key = "rec-" + randHex(8)
	}
	body := jsonObj(map[string]any{
		"namespace":       c.ns,
		"model":           c.model,
		"dimensions":      c.dims,
		"items":           []any{item},
		"ack":             *ack,
		"idempotency_key": key,
	})
	return record("PUBLISH", "POST", c.url+"/v1/publish", body, c.timeout)
}

func cmdSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	c := registerCommon(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("search: missing <query> argument")
	}
	body := jsonObj(map[string]any{
		"namespace":  c.ns,
		"model":      c.model,
		"dimensions": c.dims,
		"query":      fs.Arg(0),
	})
	return record("SEARCH", "POST", c.url+"/v1/search", body, c.timeout)
}

func cmdSubscribe(args []string) error {
	fs := flag.NewFlagSet("subscribe", flag.ExitOnError)
	c := registerCommon(fs)
	wait := fs.Duration("wait", 30*time.Second, "how long to keep the stream open and read frames")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("subscribe: missing <query> argument")
	}
	body := jsonObj(map[string]any{
		"namespace":  c.ns,
		"model":      c.model,
		"dimensions": c.dims,
		"query":      fs.Arg(0),
	})
	return recordStream("SUBSCRIBE", "POST", c.url+"/v1/subscribe", body, *wait)
}

func cmdRaw(args []string) error {
	fs := flag.NewFlagSet("raw", flag.ExitOnError)
	c := registerCommon(fs)
	method := fs.String("method", "POST", "HTTP method")
	path := fs.String("path", "", "URL path (e.g. /v1/publish)")
	body := fs.String("body", "", "raw request body")
	stream := fs.Bool("stream", false, "treat as streaming response (read frames until -timeout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("raw: -path is required")
	}
	url := c.url + *path
	if *stream {
		return recordStream("RAW", *method, url, *body, c.timeout)
	}
	return record("RAW", *method, url, *body, c.timeout)
}

// record sends a one-shot request and prints request + response.
func record(label, method, url, body string, timeout time.Duration) error {
	apiKey := os.Getenv("NOETIVE_KEY_SECRET")
	if apiKey == "" {
		return errors.New("NOETIVE_KEY_SECRET not set")
	}
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", semantik.UserAgent())

	printRequest(label, req)

	client := &http.Client{Timeout: timeout}
	t0 := time.Now()
	resp, err := client.Do(req)
	dur := time.Since(t0)
	if err != nil {
		fmt.Printf("--- TRANSPORT ERROR (%s) ---\n%v\n\n", dur, err)
		return nil
	}
	defer resp.Body.Close()

	respDump, _ := httputil.DumpResponse(resp, false)
	rb, _ := io.ReadAll(resp.Body)
	fmt.Printf("--- RESPONSE (%s) ---\n", dur)
	fmt.Print(string(respDump))
	fmt.Println(string(rb))
	fmt.Println()
	return nil
}

// recordStream sends a request and reads the response body line-by-line
// until wait elapses or EOF arrives. Useful for /v1/subscribe.
func recordStream(label, method, url, body string, wait time.Duration) error {
	apiKey := os.Getenv("NOETIVE_KEY_SECRET")
	if apiKey == "" {
		return errors.New("NOETIVE_KEY_SECRET not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()

	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", semantik.UserAgent())

	printRequest(label, req)

	t0 := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("--- TRANSPORT ERROR (%s) ---\n%v\n\n", time.Since(t0), err)
		return nil
	}
	defer resp.Body.Close()

	respDump, _ := httputil.DumpResponse(resp, false)
	fmt.Printf("--- RESPONSE HEADERS (after %s) ---\n", time.Since(t0))
	fmt.Print(string(respDump))

	fmt.Println("--- STREAM (line-buffered, until -wait elapses) ---")
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		fmt.Printf("[%s] %s\n", time.Since(t0).Round(time.Millisecond), scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		fmt.Printf("--- STREAM ERROR (%s) ---\n%v\n", time.Since(t0), err)
	} else {
		fmt.Printf("--- STREAM EOF (after %s) ---\n", time.Since(t0))
	}
	fmt.Println()
	return nil
}

func printRequest(label string, req *http.Request) {
	fmt.Printf("================ %s ================\n", label)
	dump, _ := httputil.DumpRequestOut(req, true)
	dumpStr := string(dump)
	if i := strings.Index(dumpStr, "Authorization: Bearer "); i >= 0 {
		end := strings.Index(dumpStr[i:], "\r\n")
		if end > 0 {
			dumpStr = dumpStr[:i] + "Authorization: Bearer ***REDACTED***" + dumpStr[i+end:]
		}
	}
	fmt.Println("--- REQUEST ---")
	fmt.Println(dumpStr)
}

func jsonObj(m map[string]any) string {
	var b strings.Builder
	b.WriteByte('{')
	first := true
	for k, v := range m {
		if !first {
			b.WriteByte(',')
		}
		first = false
		writeJSONString(&b, k)
		b.WriteByte(':')
		writeJSONValue(&b, v)
	}
	b.WriteByte('}')
	return b.String()
}

func writeJSONValue(b *strings.Builder, v any) {
	switch t := v.(type) {
	case string:
		writeJSONString(b, t)
	case int:
		b.WriteString(strconv.Itoa(t))
	case float64:
		b.WriteString(strconv.FormatFloat(t, 'f', -1, 64))
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONValue(b, e)
		}
		b.WriteByte(']')
	case []float64:
		b.WriteByte('[')
		for i, f := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.FormatFloat(f, 'f', -1, 64))
		}
		b.WriteByte(']')
	case map[string]any:
		b.WriteString(jsonObj(t))
	default:
		writeJSONString(b, fmt.Sprintf("%v", t))
	}
}

func writeJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

func parseFloats(s string) ([]float64, error) {
	parts := strings.Split(s, ",")
	out := make([]float64, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", i, err)
		}
		out[i] = f
	}
	return out, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
