// Package healthcheck implements pulse-api's self-check, used by the
// container's HEALTHCHECK instruction.
//
// Distroless images intentionally have no shell and no curl/wget, so a
// container-level health check can't shell out to an external tool the way
// it could on a typical base image. Instead, the same static binary that
// serves the application also acts as its own HTTP client:
// `pulse-api -healthcheck` makes one request to the running instance's
// /healthz and translates the result into a process exit code, which is
// all Docker's HEALTHCHECK actually needs.
package healthcheck

import (
	"fmt"
	"net/http"
	"time"
)

// Run issues a GET to the local instance's /healthz and returns a process
// exit code: 0 if it answered 200, 1 otherwise — including on any network
// error, since an unreachable server is exactly what a health check exists
// to detect.
func Run(port string) int {
	client := http.Client{Timeout: 2 * time.Second}

	url := fmt.Sprintf("http://127.0.0.1:%s/healthz", port)
	resp, err := client.Get(url)
	if err != nil {
		fmt.Println("healthcheck request failed:", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Println("healthcheck received non-200 status:", resp.StatusCode)
		return 1
	}
	return 0
}
