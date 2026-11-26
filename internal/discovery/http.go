package discovery

import (
	"encoding/json"
	"net/http"
)

// Handler serves the HTTP SD response. The optional query parameter port
// overrides the configured target port, so one instance can feed several
// scrape jobs (for example node_exporter on 9100 and an application on 8080).
//
// Until the first refresh succeeds it answers 503, so Prometheus keeps its
// current targets instead of dropping them. Afterwards it always answers 200
// with the latest list ([] when there are no VMs), as the HTTP SD spec requires.
func (d *Discoverer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		port := 0
		if v := r.URL.Query().Get("port"); v != "" {
			p, err := ParsePort(v)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			port = p
		}
		if !d.Ready() {
			http.Error(w, "the NodeRings VM list has not been fetched yet", http.StatusServiceUnavailable)
			return
		}
		body, err := json.Marshal(d.TargetGroups(port))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(append(body, '\n'))
	})
}
